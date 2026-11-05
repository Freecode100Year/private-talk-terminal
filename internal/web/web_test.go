package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
)

// testClient is a tiny WebSocket client: enough to drive the gateway.
type testClient struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dial(t *testing.T, addr string, hdr map[string]string) (*testClient, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n"
	for k, v := range hdr {
		req += k + ": " + v + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &testClient{t: t, c: c, br: br}, resp
}

func (tc *testClient) sendFrame(op byte, fin bool, payload []byte) {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	hdr := []byte{b0}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := []byte{1, 2, 3, 4}
	m := make([]byte, len(payload))
	for i := range payload {
		m[i] = payload[i] ^ mask[i&3]
	}
	if _, err := tc.c.Write(append(append(hdr, mask...), m...)); err != nil {
		tc.t.Fatal(err)
	}
}

func (tc *testClient) send(s string) { tc.sendFrame(opText, true, []byte(s)) }

// readUntil collects text output until it contains want.
func (tc *testClient) readUntil(want string) string {
	tc.t.Helper()
	var out strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), want) {
		_ = tc.c.SetReadDeadline(deadline)
		var h [2]byte
		if _, err := io.ReadFull(tc.br, h[:]); err != nil {
			tc.t.Fatalf("waiting for %q: %v; got %q", want, err, out.String())
		}
		n := int(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			_, _ = io.ReadFull(tc.br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			_, _ = io.ReadFull(tc.br, b[:])
			n = int(binary.BigEndian.Uint64(b[:]))
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(tc.br, p); err != nil {
			tc.t.Fatal(err)
		}
		switch h[0] & 0x0F {
		case opText:
			out.Write(p)
		case opClose:
			out.WriteString("<close>")
		}
	}
	return out.String()
}

func newTestGateway(t *testing.T, cfg chat.Config, opt Options) (string, *chat.Hub) {
	t.Helper()
	hub := chat.NewHub(cfg)
	if opt.Key == nil {
		opt.Key = func(ip net.IP) string { return ip.String() }
	}
	gw := New(hub, opt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { hub.Shutdown("bye"); cancel() })
	go func() { _ = gw.Serve(ctx, l) }()
	return l.Addr().String(), hub
}

func TestChatOverWebSocket(t *testing.T) {
	addr, _ := newTestGateway(t, chat.Config{}, Options{})
	a, resp := dial(t, addr, nil)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	a.readUntil("Welcome")
	b, _ := dial(t, addr, nil)
	b.readUntil("Welcome")

	a.send("/nick alice")
	a.readUntil("alice")
	// a fragmented message must be reassembled
	a.sendFrame(opText, false, []byte("hello "))
	a.sendFrame(opPing, true, []byte("x")) // control frames may be interleaved
	a.sendFrame(opCont, true, []byte("from the web 你好"))
	b.readUntil("<alice> hello from the web 你好")

	a.send("/quit")
	a.readUntil("<close>")
}

func TestForeignOriginRefused(t *testing.T) {
	addr, _ := newTestGateway(t, chat.Config{}, Options{AllowOrigins: []string{"https://ok.example"}})
	_, resp := dial(t, addr, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d, want 403", resp.StatusCode)
	}
	_, resp = dial(t, addr, map[string]string{"Origin": "https://ok.example"})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("allowed origin: status %d", resp.StatusCode)
	}
	_, resp = dial(t, addr, map[string]string{"Origin": "http://" + addr})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("same origin: status %d", resp.StatusCode)
	}
}

func TestPerAddressLimitAndProxyHeader(t *testing.T) {
	addr, _ := newTestGateway(t, chat.Config{MaxConnsPerIP: 1}, Options{ProxyHeader: "X-Forwarded-For"})
	a, _ := dial(t, addr, map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.1"})
	a.readUntil("Welcome")
	// same nearest-proxy address → over the limit
	b, _ := dial(t, addr, map[string]string{"X-Forwarded-For": "198.51.100.1"})
	if out := b.readUntil("<close>"); !strings.Contains(out, "Sorry") {
		t.Fatalf("expected a refusal, got %q", out)
	}
	// a different address is fine
	c, _ := dial(t, addr, map[string]string{"X-Forwarded-For": "198.51.100.2"})
	c.readUntil("Welcome")
}

func TestOversizeAndUnmaskedFramesDropped(t *testing.T) {
	addr, hub := newTestGateway(t, chat.Config{}, Options{})
	a, _ := dial(t, addr, nil)
	a.readUntil("Welcome")
	a.sendFrame(opText, true, make([]byte, maxInput+1))
	a.readUntil("<close>")

	b, _ := dial(t, addr, nil)
	b.readUntil("Welcome")
	_, _ = b.c.Write([]byte{0x81, 0x02, 'h', 'i'}) // unmasked: protocol error
	b.readUntil("<close>")

	deadline := time.Now().Add(2 * time.Second)
	for hub.Stats().Online != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := hub.Stats().Online; n != 0 {
		t.Fatalf("%d sessions still online", n)
	}
}

func TestPageAndHeaders(t *testing.T) {
	gw := New(chat.NewHub(chat.Config{}), Options{Key: func(net.IP) string { return "k" }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, p := range []string{"/", "/app.js", "/style.css", "/healthz"} {
		rec := httptest.NewRecorder()
		gw.Handler().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("%s: missing CSP", p)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s: sets a cookie", p)
		}
	}
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Errorf("/nope: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/ws", nil))
	if rec.Code != http.StatusUpgradeRequired {
		t.Errorf("/ws without upgrade: %d", rec.Code)
	}
}
