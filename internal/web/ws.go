package web

// A minimal RFC 6455 server side: enough for a chat (text frames, ping/pong,
// close, fragmentation), with no third-party dependency. Binary frames and
// extensions (permessage-deflate) are not negotiated.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opCont  = 0x0
	opText  = 0x1
	opBin   = 0x2
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

var errTooBig = errors.New("websocket: message too large")

// wsConn is one upgraded connection. Reads happen on a single goroutine;
// writes may come from several and are serialised by mu.
type wsConn struct {
	c      net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	maxMsg int
	idle   time.Duration // close if no frame (incl. pong) arrives for this long
	closed bool
}

func headerHas(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

// upgrade performs the opening handshake. On failure it has already written
// an HTTP error response.
func upgrade(w http.ResponseWriter, r *http.Request, maxMsg int) (*wsConn, error) {
	if r.Method != http.MethodGet || !headerHas(r.Header, "Connection", "upgrade") ||
		!headerHas(r.Header, "Upgrade", "websocket") {
		http.Error(w, "需要 WebSocket · WebSocket upgrade required", http.StatusUpgradeRequired)
		return nil, errors.New("not a websocket request")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "unsupported websocket version", http.StatusUpgradeRequired)
		return nil, errors.New("bad websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		http.Error(w, "bad Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("bad websocket key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server cannot upgrade", http.StatusInternalServerError)
		return nil, errors.New("no hijacker")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{c: conn, br: brw.Reader, maxMsg: maxMsg}, nil
}

// writeFrame sends one unfragmented, unmasked frame.
func (ws *wsConn) writeFrame(op byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return net.ErrClosed
	}
	hdr := make([]byte, 0, 10)
	hdr = append(hdr, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	_ = ws.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := ws.c.Write(append(hdr, payload...)); err != nil {
		ws.closed = true
		ws.c.Close()
		return err
	}
	return nil
}

// Ping sends a ping; the client's pong refreshes the read deadline.
func (ws *wsConn) Ping() error { return ws.writeFrame(opPing, nil) }

// WriteText sends s as one text message.
func (ws *wsConn) WriteText(s string) error { return ws.writeFrame(opText, []byte(s)) }

// Close sends a close frame (best effort) and closes the connection.
func (ws *wsConn) Close(code uint16, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	p := binary.BigEndian.AppendUint16(nil, code)
	_ = ws.writeFrame(opClose, append(p, reason...))
	ws.mu.Lock()
	ws.closed = true
	ws.mu.Unlock()
	ws.c.Close()
}

// ReadMessage returns the next complete text message. Control frames are
// handled internally. A close frame or any protocol error returns an error.
func (ws *wsConn) ReadMessage() (string, error) {
	var msg []byte
	started := false
	for {
		var h [2]byte
		if ws.idle > 0 {
			_ = ws.c.SetReadDeadline(time.Now().Add(ws.idle))
		}
		if _, err := io.ReadFull(ws.br, h[:]); err != nil {
			return "", err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0F
		if h[0]&0x70 != 0 {
			return "", errors.New("websocket: reserved bits set")
		}
		if h[1]&0x80 == 0 {
			return "", errors.New("websocket: client frames must be masked")
		}
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(ws.br, b[:]); err != nil {
				return "", err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(ws.br, b[:]); err != nil {
				return "", err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		isCtl := op&0x8 != 0
		if isCtl && (n > 125 || !fin) {
			return "", errors.New("websocket: bad control frame")
		}
		if !isCtl && uint64(len(msg))+n > uint64(ws.maxMsg) {
			return "", errTooBig
		}
		var mask [4]byte
		if _, err := io.ReadFull(ws.br, mask[:]); err != nil {
			return "", err
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(ws.br, payload); err != nil {
			return "", err
		}
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
		switch op {
		case opPing:
			if err := ws.writeFrame(opPong, payload); err != nil {
				return "", err
			}
			continue
		case opPong:
			continue
		case opClose:
			return "", io.EOF
		case opText, opBin:
			if started {
				return "", errors.New("websocket: expected continuation frame")
			}
			if op == opBin {
				return "", errors.New("websocket: binary messages not supported")
			}
			started = true
			msg = payload
		case opCont:
			if !started {
				return "", errors.New("websocket: unexpected continuation frame")
			}
			msg = append(msg, payload...)
		default:
			return "", errors.New("websocket: unknown opcode")
		}
		if fin {
			if !utf8.Valid(msg) {
				return "", errors.New("websocket: invalid UTF-8")
			}
			return string(msg), nil
		}
	}
}
