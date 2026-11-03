// Package server wires the chat hub to an SSH listener using Wish.
//
// Security posture:
//   - No shell, no exec, no SFTP/SCP, no port/agent/X11 forwarding: the only
//     thing an SSH session can reach is the chat UI.
//   - Anonymous login: any username, no password. Public keys are only
//     accepted when they are listed as admin keys; everyone else falls
//     through to an empty keyboard-interactive exchange.
//   - Client IP addresses are never logged or stored. Rate limits and bans
//     use an HMAC of the address (IPv6 grouped by /64) under a random key
//     that only lives in memory and changes on every restart.
package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/bubbletea"
	gossh "golang.org/x/crypto/ssh"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
	"github.com/Freecode100Year/private-talk-terminal/internal/ui"
)

// Options configures the SSH front-end.
type Options struct {
	Addr          string
	HostKeyPath   string
	AdminKeysPath string        // authorized_keys-format file; optional
	IdleTimeout   time.Duration // disconnect after this long without traffic
	MaxTimeout    time.Duration // absolute session limit (0 = none)
	Banner        string
	MaxTCPPerIP   int // raw TCP connections per address, incl. unauthenticated (default 8)
}

type ctxKey int

const clientKey ctxKey = 1

// Server is a running chat SSH server.
type Server struct {
	*ssh.Server
	hub    *chat.Hub
	salt   []byte
	admins [][]byte
	log    *slog.Logger

	tcpMu    sync.Mutex
	tcpConns map[string]int
	tcpMax   int
}

// New builds the SSH server around hub.
func New(hub *chat.Hub, opt Options, log *slog.Logger) (*Server, error) {
	s := &Server{hub: hub, log: log, salt: make([]byte, 32), tcpConns: map[string]int{}, tcpMax: opt.MaxTCPPerIP}
	if s.tcpMax <= 0 {
		s.tcpMax = 8
	}
	if _, err := rand.Read(s.salt); err != nil {
		return nil, err
	}
	if opt.AdminKeysPath != "" {
		keys, err := loadAuthorizedKeys(opt.AdminKeysPath)
		if err != nil {
			return nil, fmt.Errorf("admin keys: %w", err)
		}
		s.admins = keys
		log.Info("loaded admin keys", "count", len(keys))
	}

	opts := []ssh.Option{
		wish.WithAddress(opt.Addr),
		wish.WithHostKeyPath(opt.HostKeyPath),
		wish.WithVersion("PrivateTalk"),
		wish.WithPublicKeyAuth(func(_ ssh.Context, key ssh.PublicKey) bool {
			return s.isAdmin(key)
		}),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool {
			return true
		}),
		wish.WithPasswordAuth(func(ssh.Context, string) bool { return true }),
		wish.WithMiddleware(
			bubbletea.Middleware(s.teaHandler), // innermost
			s.plainMiddleware,
			s.sessionMiddleware, // outermost, runs first
		),
	}
	if opt.IdleTimeout > 0 {
		opts = append(opts, wish.WithIdleTimeout(opt.IdleTimeout))
	}
	if opt.MaxTimeout > 0 {
		opts = append(opts, wish.WithMaxTimeout(opt.MaxTimeout))
	}
	if opt.Banner != "" {
		opts = append(opts, wish.WithBanner(opt.Banner))
	}
	srv, err := wish.NewServer(opts...)
	if err != nil {
		return nil, err
	}
	// Belt and braces: make sure nothing but interactive sessions exists.
	srv.LocalPortForwardingCallback = func(ssh.Context, string, uint32) bool { return false }
	srv.ReversePortForwardingCallback = func(ssh.Context, string, uint32) bool { return false }
	srv.SubsystemHandlers = map[string]ssh.SubsystemHandler{}
	srv.ChannelHandlers = map[string]ssh.ChannelHandler{"session": ssh.DefaultSessionHandler}
	srv.RequestHandlers = map[string]ssh.RequestHandler{}
	srv.ConnCallback = s.limitConn
	s.Server = srv
	return s, nil
}

// Hub returns the chat hub.
func (s *Server) Hub() *chat.Hub { return s.hub }

func (s *Server) isAdmin(key ssh.PublicKey) bool {
	if key == nil || len(s.admins) == 0 {
		return false
	}
	m := key.Marshal()
	for _, a := range s.admins {
		if hmac.Equal(a, m) {
			return true
		}
	}
	return false
}

// addrKey turns a remote address into an opaque key. IPv6 is grouped by /64
// so a single host can't dodge limits by rotating through its prefix.
func (s *Server) addrKey(addr net.Addr) string {
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	var b []byte
	switch {
	case ip == nil:
		b = []byte(host)
	case ip.To4() != nil:
		b = ip.To4()
	default:
		b = ip.Mask(net.CIDRMask(64, 128))
	}
	mac := hmac.New(sha256.New, s.salt)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

// sessionMiddleware rejects anything that isn't a plain interactive login,
// registers the client with the hub and cleans up when the session ends.
func (s *Server) sessionMiddleware(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		if len(sess.Command()) > 0 || sess.Subsystem() != "" {
			wish.Fatalln(sess, "This server only offers the chat room. Connect without a command:  ssh -p 2222 guest@<host>")
			return
		}
		admin := s.isAdmin(sess.PublicKey())
		c, err := s.hub.Connect(s.addrKey(sess.RemoteAddr()), admin)
		if err != nil {
			wish.Fatalln(sess, "Sorry: "+err.Error())
			return
		}
		if admin {
			s.log.Info("admin session started")
		}
		sess.Context().SetValue(clientKey, c)
		defer s.hub.Disconnect(c)

		// If the hub drops the client (kick/ban), close the SSH session too.
		go func() {
			select {
			case <-c.Done:
				time.Sleep(500 * time.Millisecond) // let the UI print the reason
				_ = sess.Close()
			case <-sess.Context().Done():
			}
		}()
		next(sess)
	}
}

// limitConn caps raw TCP connections per address, so nobody can hold
// hundreds of idle or half-open connections without ever opening a session.
func (s *Server) limitConn(_ ssh.Context, conn net.Conn) net.Conn {
	key := s.addrKey(conn.RemoteAddr())
	s.tcpMu.Lock()
	defer s.tcpMu.Unlock()
	if s.tcpConns[key] >= s.tcpMax {
		return nil
	}
	s.tcpConns[key]++
	return &countedConn{Conn: conn, release: func() {
		s.tcpMu.Lock()
		defer s.tcpMu.Unlock()
		if s.tcpConns[key] <= 1 {
			delete(s.tcpConns, key)
		} else {
			s.tcpConns[key]--
		}
	}}
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func clientFrom(sess ssh.Session) *chat.Client {
	c, _ := sess.Context().Value(clientKey).(*chat.Client)
	return c
}

// wantsPlain decides between the TUI and line mode.
func wantsPlain(sess ssh.Session) (plain bool, hasPty bool, width int) {
	pty, _, ok := sess.Pty()
	if !ok {
		return true, false, 80
	}
	user := strings.ToLower(sess.User())
	term := strings.ToLower(pty.Term)
	if user == "plain" || user == "text" || term == "dumb" || term == "" {
		return true, true, pty.Window.Width
	}
	return false, true, pty.Window.Width
}

func (s *Server) plainMiddleware(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		plain, hasPty, width := wantsPlain(sess)
		if !plain {
			next(sess)
			return
		}
		c := clientFrom(sess)
		if c == nil {
			return
		}
		ui.Plain(sess, s.hub, c, hasPty, width)
		_ = sess.Exit(0)
	}
}

func (s *Server) teaHandler(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
	c := clientFrom(sess)
	if c == nil {
		return nil, nil
	}
	pty, _, _ := sess.Pty()
	return ui.NewTUI(s.hub, c, pty.Window.Width, pty.Window.Height), nil
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.hub.Janitor()
			}
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.hub.Shutdown("Server is restarting — please reconnect in a moment. 服务器重启中，请稍后重连。")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(sctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		return err
	}
	return nil
}

func loadAuthorizedKeys(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", line, err)
		}
		keys = append(keys, k.Marshal())
	}
	return keys, sc.Err()
}
