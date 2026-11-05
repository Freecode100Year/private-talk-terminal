package server

// A minimal, dependency-free WebSocket (RFC 6455) server side, taken from
// the HTML Room broadcast relay (github.com/Freecode100Year/htmlroom,
// relay/go/main.go) so VIVANT keeps a single static binary with no new
// modules. Text frames only, no extensions, server frames unmasked.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opCont  = 0x0
	opText  = 0x1
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

var errProtocol = errors.New("websocket protocol error")

func headerHas(r *http.Request, name, token string) bool {
	for _, v := range r.Header.Values(name) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

func isUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet && headerHas(r, "Connection", "upgrade") && headerHas(r, "Upgrade", "websocket") &&
		r.Header.Get("Sec-WebSocket-Key") != "" && r.Header.Get("Sec-WebSocket-Version") == "13"
}

// wsAccept hijacks the connection and completes the handshake.
func wsAccept(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.Reader, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("cannot hijack")
	}
	nc, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	if err := rw.Flush(); err != nil {
		nc.Close()
		return nil, nil, err
	}
	return nc, rw.Reader, nil
}

func closePayload(code uint16, reason string) []byte {
	b := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(b, code)
	return append(b, reason...)
}

// writeFrame writes one unmasked, single-fragment frame.
func writeFrame(w io.Writer, op byte, payload []byte) error {
	head := make([]byte, 0, 10+len(payload))
	head = append(head, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		head = append(head, byte(n))
	case n < 65536:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	_, err := w.Write(append(head, payload...))
	return err
}

// readFrame reads one client frame (clients must mask) of at most limit bytes.
func readFrame(r *bufio.Reader, limit int) (fin bool, op byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return
	}
	fin, op = head[0]&0x80 != 0, head[0]&0x0f
	if head[0]&0x70 != 0 || head[1]&0x80 == 0 { // no extensions; clients must mask
		return false, 0, nil, errProtocol
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > uint64(limit) || (op >= 0x8 && (n > 125 || !fin)) {
		return false, 0, nil, errProtocol
	}
	var mask [4]byte
	if _, err = io.ReadFull(r, mask[:]); err != nil {
		return
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return
}
