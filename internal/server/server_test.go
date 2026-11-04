package server

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

func startServer(t *testing.T) string {
	t.Helper()
	return startServerWith(t, nil)
}

func startServerWith(t *testing.T, mod func(*Options)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	hub := chat.NewHub(chat.Config{MsgBurst: 50, MsgPerSecond: 50})
	opt := Options{Addr: addr, HostKeyPath: filepath.Join(t.TempDir(), "hk")}
	if mod != nil {
		mod(&opt)
	}
	srv, err := New(hub, opt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)
	t.Cleanup(cancel)
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return addr
}

func dial(t *testing.T, addr string) *gossh.Client {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User:            "guest",
		Auth:            []gossh.AuthMethod{gossh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	c, err := gossh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// plainSession opens a no-PTY session (line mode) and returns stdin + a line reader.
func plainSession(t *testing.T, c *gossh.Client) (io.Writer, *bufio.Scanner) {
	t.Helper()
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.StdinPipe()
	out, _ := s.StdoutPipe()
	if err := s.Shell(); err != nil {
		t.Fatal(err)
	}
	return in, bufio.NewScanner(out)
}

func waitFor(t *testing.T, sc *bufio.Scanner, substr string) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		for sc.Scan() {
			if strings.Contains(sc.Text(), substr) {
				done <- true
				return
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("stream ended before %q", substr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %q", substr)
	}
}

func TestEndToEndChat(t *testing.T) {
	addr := startServer(t)
	inA, outA := plainSession(t, dial(t, addr))
	_, outB := plainSession(t, dial(t, addr))
	waitFor(t, outA, "Now in #general")
	waitFor(t, outB, "Now in #general")

	io.WriteString(inA, "/nick Alice\n")
	io.WriteString(inA, "你好 over ssh\n")
	waitFor(t, outB, "<Alice> 你好 over ssh")
}

func TestExecRejected(t *testing.T) {
	addr := startServer(t)
	s, err := dial(t, addr).NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := s.CombinedOutput("cat /etc/passwd")
	if !strings.Contains(string(out), "only offers the chat room") || strings.Contains(string(out), "root:") {
		t.Fatalf("exec not rejected: %q", out)
	}
}

func TestForwardingRejected(t *testing.T) {
	addr := startServer(t)
	c := dial(t, addr)
	if conn, err := c.Dial("tcp", addr); err == nil {
		conn.Close()
		t.Fatal("direct-tcpip forwarding allowed")
	}
	if l, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		t.Fatal("remote forwarding allowed")
	}
}

// Ordinary users must be admitted before their client offers any public key:
// keys are linkable identities.
func TestClientNeverOffersKey(t *testing.T) {
	addr := startServer(t)
	signer := testSigner(t)
	offered := false
	cfg := &gossh.ClientConfig{
		User: "guest",
		Auth: []gossh.AuthMethod{gossh.PublicKeysCallback(func() ([]gossh.Signer, error) {
			offered = true
			return []gossh.Signer{signer}, nil
		})},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	c, err := gossh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if offered {
		t.Fatal("server let the client get as far as offering a public key")
	}
}

func TestAdminRequiresKey(t *testing.T) {
	adminSigner := testSigner(t)
	addr := startServerWith(t, func(o *Options) {
		path := filepath.Join(t.TempDir(), "admins")
		os.WriteFile(path, gossh.MarshalAuthorizedKey(adminSigner.PublicKey()), 0o600)
		o.AdminKeysPath = path
	})
	try := func(user string, auth ...gossh.AuthMethod) error {
		c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{User: user, Auth: auth, HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := try("admin", gossh.PublicKeys(adminSigner)); err != nil {
		t.Fatalf("admin with key rejected: %v", err)
	}
	if err := try("admin"); err == nil {
		t.Fatal("admin username accepted without a key")
	}
	if err := try("admin", gossh.PublicKeys(testSigner(t))); err == nil {
		t.Fatal("admin username accepted with an unlisted key")
	}
	if err := try("admin", gossh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })); err == nil {
		t.Fatal("admin username accepted via keyboard-interactive")
	}
}

func testSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A ban must survive a restart when -ban-file is set: same HMAC key, same file.
func TestBanPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	banFile := filepath.Join(dir, "bans.json")
	mk := func() *Server {
		hub := chat.NewHub(chat.Config{})
		srv, err := New(hub, Options{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(dir, "hk"), BanFile: banFile},
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	addr := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5555}

	s1 := mk()
	key := s1.addrKey(addr)
	adm, _ := s1.hub.Connect("admin-key", true)
	victim, _ := s1.hub.Connect(key, false)
	s1.hub.Handle(adm, "/ban "+s1.hub.Nick(victim)+" 60")
	bans, _ := s1.hub.Bans()
	if err := writeBans(banFile, bans); err != nil { // what persistBans does
		t.Fatal(err)
	}
	if fi, _ := os.Stat(banFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("ban file mode %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "ban.key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("ban key mode %v, want 0600", fi.Mode().Perm())
	}

	s2 := mk()
	if s2.addrKey(addr) != key {
		t.Fatal("address key changed across restart")
	}
	if _, err := s2.hub.Connect(s2.addrKey(addr), false); err != chat.ErrBanned {
		t.Fatalf("ban lost after restart: %v", err)
	}
	data, _ := os.ReadFile(banFile)
	if strings.Contains(string(data), "203.0.113.7") {
		t.Fatal("raw address written to disk")
	}
}

// Without a ban file nothing is persisted and keys change every start.
func TestEphemeralKeysByDefault(t *testing.T) {
	mk := func() *Server {
		srv, err := New(chat.NewHub(chat.Config{}), Options{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(t.TempDir(), "hk")},
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	addr := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 1}
	if mk().addrKey(addr) == mk().addrKey(addr) {
		t.Fatal("address keys are stable across restarts without -ban-file")
	}
}

func TestAlternateListener(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	alt := l.Addr().String()
	l.Close()
	startServerWith(t, func(o *Options) { o.AltAddrs = []string{alt} })
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", alt); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	in, out := plainSession(t, dial(t, alt))
	waitFor(t, out, "Now in #general")
	_ = in
}

func TestTorExitsShareTorBucket(t *testing.T) {
	srv, err := New(chat.NewHub(chat.Config{}), Options{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(t.TempDir(), "hk")},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ips := parseExitList(strings.NewReader("# Tor exits\n185.220.101.1\n2001:db8::7\nnot-an-ip\n\n"))
	if len(ips) != 2 {
		t.Fatalf("parsed %v", ips)
	}
	srv.setTorExits(ips)
	local := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2222}
	exit := &net.TCPAddr{IP: net.ParseIP("185.220.101.1"), Port: 40000}
	mapped := &net.TCPAddr{IP: net.ParseIP("::ffff:185.220.101.1"), Port: 40000}
	normal := &net.TCPAddr{IP: net.ParseIP("203.0.113.5"), Port: 40000}
	if srv.keyFor(local, exit) != OnionKey || srv.keyFor(local, mapped) != OnionKey {
		t.Fatal("Tor exit not mapped to the shared Tor bucket")
	}
	if srv.keyFor(local, normal) == OnionKey {
		t.Fatal("ordinary address mapped to the Tor bucket")
	}
}
