package server

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wsClient is a tiny browser-like WebSocket client for tests.
type wsClient struct {
	t  *testing.T
	nc net.Conn
	br *bufio.Reader
}

func wsDial(t *testing.T, addr, origin string) *wsClient {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	key := make([]byte, 16)
	rand.Read(key)
	req := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\nSec-WebSocket-Version: 13\r\n"
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	io.WriteString(nc, req+"\r\n")
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: %s", resp.Status)
	}
	return &wsClient{t: t, nc: nc, br: br}
}

func (c *wsClient) send(v any) {
	b, _ := json.Marshal(v)
	mask := []byte{1, 2, 3, 4}
	head := []byte{0x81}
	if len(b) < 126 {
		head = append(head, 0x80|byte(len(b)))
	} else {
		head = append(head, 0x80|126, byte(len(b)>>8), byte(len(b)))
	}
	head = append(head, mask...)
	for i := range b {
		b[i] ^= mask[i%4]
	}
	c.nc.Write(append(head, b...))
}

// next returns the next JSON message (skipping pings).
func (c *wsClient) next() map[string]any {
	c.t.Helper()
	for {
		c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			c.t.Fatalf("read: %v", err)
		}
		n := int(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			io.ReadFull(c.br, b[:])
			n = int(b[0])<<8 | int(b[1])
		case 127:
			var b [8]byte
			io.ReadFull(c.br, b[:])
			n = int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		}
		p := make([]byte, n)
		io.ReadFull(c.br, p)
		switch h[0] & 0x0f {
		case opText:
			var m map[string]any
			if err := json.Unmarshal(p, &m); err != nil {
				c.t.Fatal(err)
			}
			return m
		case opClose:
			return map[string]any{"t": "closed"}
		}
	}
}

// waitMsg reads until pred matches.
func (c *wsClient) waitMsg(what string, pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := c.next()
		if pred(m) {
			return m
		}
		if m["t"] == "closed" {
			break
		}
	}
	c.t.Fatalf("never got %s", what)
	return nil
}

func isEv(substr string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		return m["t"] == "ev" && (strings.Contains(fmt.Sprint(m["text"]), substr) || strings.Contains(fmt.Sprint(m["en"]), substr))
	}
}

func startWeb(t *testing.T, mod func(*Options)) (sshAddr, webAddr string) {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	webAddr = l.Addr().String()
	l.Close()
	sshAddr = startServerWith(t, func(o *Options) {
		o.WebAddr, o.Version, o.WebSSH = webAddr, "v-test", "ssh -p 2222 guest@example.org"
		if mod != nil {
			mod(o)
		}
	})
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", webAddr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return
}

func TestWebAndSSHShareTheRoom(t *testing.T) {
	sshAddr, webAddr := startWeb(t, nil)
	w := wsDial(t, webAddr, "http://"+webAddr)
	hello := w.next()
	if hello["t"] != "hello" || hello["version"] != "v-test" || hello["palettes"] == nil {
		t.Fatalf("hello: %v", hello)
	}
	st := w.waitMsg("status", func(m map[string]any) bool { return m["t"] == "st" })
	if st["font"].(float64) != 16 || st["theme"] != "green" {
		t.Fatalf("status: %v", st)
	}

	inS, outS := plainSession(t, dial(t, sshAddr))
	waitFor(t, outS, "已进入 #general")

	w.send(map[string]string{"t": "say", "text": "/nick WebUser"})
	w.waitMsg("nick status", func(m map[string]any) bool { return m["t"] == "st" && m["nick"] == "WebUser" })
	w.send(map[string]string{"t": "say", "text": "hello from the browser 你好"})
	waitFor(t, outS, "<WebUser> hello from the browser 你好")

	io.WriteString(inS, "/nick SshUser\n")
	io.WriteString(inS, "hi @WebUser\n")
	ev := w.waitMsg("mention from ssh", isEv("hi @WebUser"))
	if ev["from"] != "SshUser" || ev["mention"] != true || ev["ring"] != true || ev["kind"] != "chat" {
		t.Fatalf("event: %v", ev)
	}

	// /font works for web users and is reported in the status
	w.send(map[string]string{"t": "font", "v": "+"})
	w.waitMsg("font 18", func(m map[string]any) bool { return m["t"] == "st" && m["font"].(float64) == 18 })
	w.send(map[string]string{"t": "say", "text": "/font 24"})
	w.waitMsg("font 24", func(m map[string]any) bool { return m["t"] == "st" && m["font"].(float64) == 24 })
	w.send(map[string]string{"t": "theme", "v": "dark"})
	w.waitMsg("theme", func(m map[string]any) bool { return m["t"] == "st" && m["theme"] == "dark" })

	// /quit ends the session cleanly
	w.send(map[string]string{"t": "say", "text": "/quit"})
	w.waitMsg("bye", func(m map[string]any) bool { return m["t"] == "bye" })
	waitFor(t, outS, "WebUser")
}

func TestWebRejectsForeignOrigin(t *testing.T) {
	_, webAddr := startWeb(t, nil)
	nc, err := net.Dial("tcp", webAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	io.WriteString(nc, "GET /ws HTTP/1.1\r\nHost: "+webAddr+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: https://evil.example\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(nc), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %s", resp.Status)
	}
}

func TestWebPageHeaders(t *testing.T) {
	_, webAddr := startWeb(t, nil)
	for _, path := range []string{"/", "/app.js", "/app.css", "/icon.svg"} {
		resp, err := http.Get("http://" + webAddr + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) == 0 {
			t.Fatalf("%s: %s", path, resp.Status)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
			resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: headers %v", path, resp.Header)
		}
	}
	if resp, _ := http.Get("http://" + webAddr + "/nope"); resp.StatusCode != 404 {
		t.Fatalf("/nope: %s", resp.Status)
	}
}

func TestWebPerAddressLimit(t *testing.T) {
	_, webAddr := startWeb(t, nil)
	for i := 0; i < 3; i++ { // default -max-per-ip 3
		w := wsDial(t, webAddr, "")
		w.waitMsg("status", func(m map[string]any) bool { return m["t"] == "st" })
	}
	w := wsDial(t, webAddr, "")
	bye := w.waitMsg("refusal", func(m map[string]any) bool { return m["t"] == "bye" })
	if !strings.Contains(bye["reason"].(string), "连接数太多") {
		t.Fatalf("refusal: %v", bye)
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, tc := range []struct {
		mode, remote string
		hdr          map[string]string
		want         string
	}{
		{ProxyNone, "203.0.113.5:1234", map[string]string{"X-Forwarded-For": "1.1.1.1"}, "203.0.113.5"},
		{ProxyLocal, "127.0.0.1:1234", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		{ProxyLocal, "203.0.113.5:1234", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "203.0.113.5"}, // not from a local proxy
		{ProxyCloudflare, "172.70.1.1:1234", map[string]string{"CF-Connecting-IP": "198.51.100.9"}, "198.51.100.9"},
		{ProxyCloudflare, "[::1]:1234", map[string]string{"CF-Connecting-IP": "2001:db8::1"}, "2001:db8::1"},        // cloudflared
		{ProxyCloudflare, "203.0.113.5:1234", map[string]string{"CF-Connecting-IP": "198.51.100.9"}, "203.0.113.5"}, // spoofed, direct
	} {
		if got := clientIP(req(tc.remote, tc.hdr), tc.mode); got.String() != tc.want {
			t.Errorf("%s %s %v: got %s, want %s", tc.mode, tc.remote, tc.hdr, got, tc.want)
		}
	}
	if _, err := ParseProxyMode("bogus"); err == nil {
		t.Fatal("bogus proxy mode accepted")
	}
}
