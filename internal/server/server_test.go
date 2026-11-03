package server

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

func startServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	hub := chat.NewHub(chat.Config{MsgBurst: 50, MsgPerSecond: 50})
	srv, err := New(hub, Options{Addr: addr, HostKeyPath: filepath.Join(t.TempDir(), "hk")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
