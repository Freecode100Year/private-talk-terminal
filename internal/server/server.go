// Package server wires the chat hub to an SSH listener using Wish.
//
// Security posture:
//   - No shell, no exec, no SFTP/SCP, no port/agent/X11 forwarding: the only
//     thing an SSH session can reach is the chat UI.
//   - Anonymous login: any username, no password. Ordinary users are let in
//     at the SSH "none" authentication step, so their client never offers a
//     public key — keys are linkable identities (e.g. github.com/<user>.keys).
//     Only the admin username goes on to public-key authentication.
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
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/ssh"
	"charm.land/wish/v2"
	gossh "golang.org/x/crypto/ssh"

	"github.com/Freecode100Year/vivant/internal/chat"
	"github.com/Freecode100Year/vivant/internal/ui"
)

// Options configures the SSH front-end.
type Options struct {
	Addr          string
	HostKeyPath   string
	AdminKeysPath string        // authorized_keys-format file; optional
	IdleTimeout   time.Duration // disconnect after this long without traffic
	MaxTimeout    time.Duration // absolute session limit (0 = none)
	Banner        string
	MaxTCPPerIP   int      // raw TCP connections per address, incl. unauthenticated (default 8)
	MaxTCP        int      // raw TCP connections in total, incl. unauthenticated (default 2500)
	ReadRate      int      // bytes/s a connection may send us; excess is slowed down, not dropped (0 = off)
	ReadBurst     int      // bytes allowed at full speed before ReadRate applies
	AdminUser     string   // username that authenticates with an admin key (default "admin")
	OnionAddr     string   // optional second listener for a Tor onion service (e.g. 127.0.0.1:2224)
	OnionMaxTCP   int      // TCP connections allowed through the onion listener (default 400)
	BanFile       string   // optional: persist bans here (see bans.go for the privacy trade-off)
	AltAddrs      []string // extra listen addresses, e.g. ":443" for networks that block other ports
	TorExits      bool     // fetch Tor's exit list so Tor Browser users share the Tor bucket
}

// OnionKey is the shared rate-limit key for everyone arriving via Tor: the
// server can't tell onion users apart, which is the point.
const OnionKey = chat.OnionKey

type ctxKey int

const (
	clientKey ctxKey = 1
	connKey   ctxKey = 2
)

// sshAlgorithms drops the legacy choices golang.org/x/crypto still offers by
// default (SHA-1 key exchange and MACs) and the finite-field Diffie-Hellman
// groups, whose 2048–4096-bit modular exponentiation is the most CPU a
// client can make the server spend before it even authenticates. Every SSH
// client from the last decade (OpenSSH ≥ 6.5, PuTTY ≥ 0.68, Termius, iSH)
// speaks curve25519.
var sshAlgorithms = gossh.Config{
	KeyExchanges: []string{
		gossh.KeyExchangeMLKEM768X25519, // post-quantum hybrid
		gossh.KeyExchangeCurve25519,
		gossh.KeyExchangeECDHP256,
		gossh.KeyExchangeECDHP384,
		gossh.KeyExchangeECDHP521,
	},
	Ciphers: []string{
		gossh.CipherChaCha20Poly1305,
		gossh.CipherAES256GCM,
		gossh.CipherAES128GCM,
		gossh.CipherAES256CTR,
		gossh.CipherAES192CTR,
		gossh.CipherAES128CTR,
	},
	MACs: []string{
		gossh.HMACSHA256ETM,
		gossh.HMACSHA512ETM,
		gossh.HMACSHA256,
		gossh.HMACSHA512,
	},
}

// handshakeTimeout: a connection that hasn't opened a chat session by then is
// closed. Real clients take well under a second (a few over Tor); port
// scanners and stalled handshakes would otherwise hold a connection slot
// until the idle timeout, an hour later.
var handshakeTimeout = 30 * time.Second

// Server is a running chat SSH server.
type Server struct {
	*ssh.Server
	hub    *chat.Hub
	salt   []byte
	admins [][]byte
	log    *slog.Logger

	tcpMu     sync.Mutex
	tcpConns  map[string]int
	tcpMax    int
	tcpTotal  int
	tcpAll    int // cap on tcpTotal
	readRate  float64
	readBurst float64

	adminUser  string
	onionAddr  string
	onionMax   int
	banFile    string
	altAddrs   []string
	torExitsOn bool
	torExits   atomic.Pointer[exitSet]
}

// New builds the SSH server around hub.
func New(hub *chat.Hub, opt Options, log *slog.Logger) (*Server, error) {
	s := &Server{hub: hub, log: log, salt: make([]byte, 32), tcpConns: map[string]int{}, tcpMax: opt.MaxTCPPerIP,
		adminUser: strings.ToLower(opt.AdminUser), onionAddr: opt.OnionAddr, onionMax: opt.OnionMaxTCP, altAddrs: opt.AltAddrs, torExitsOn: opt.TorExits}
	if s.tcpMax <= 0 {
		s.tcpMax = 8
	}
	if s.tcpAll = opt.MaxTCP; s.tcpAll <= 0 {
		s.tcpAll = 2500
	}
	if opt.ReadRate > 0 {
		s.readRate, s.readBurst = float64(opt.ReadRate), float64(max(opt.ReadBurst, opt.ReadRate))
	}
	if s.adminUser == "" {
		s.adminUser = "admin"
	}
	if s.onionMax <= 0 {
		s.onionMax = 400
	}
	if _, err := rand.Read(s.salt); err != nil {
		return nil, err
	}
	if opt.BanFile != "" {
		bans, err := readBans(opt.BanFile)
		if err != nil {
			return nil, fmt.Errorf("ban file: %w", err)
		}
		keyPath := filepath.Join(filepath.Dir(opt.BanFile), "ban.key")
		if activeBans(bans, time.Now()) == 0 {
			// Nothing to carry over: start with a fresh key, so the old one
			// (and any copy of it) can't be used against past bans.
			if err := os.Remove(keyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("ban key: %w", err)
			}
			bans = nil
		}
		salt, err := loadOrCreateSalt(keyPath)
		if err != nil {
			return nil, fmt.Errorf("ban key: %w", err)
		}
		s.salt = salt
		s.banFile = opt.BanFile
		log.Info("ban persistence enabled", "loaded", hub.LoadBans(bans))
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
		wish.WithVersion("VIVANT"),
		// Only reached by the admin username (everyone else is accepted at
		// "none"): require a listed key, refuse every other method.
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			return s.isAdminUser(ctx.User()) && s.isAdmin(key)
		}),
		wish.WithKeyboardInteractiveAuth(func(ctx ssh.Context, _ gossh.KeyboardInteractiveChallenge) bool {
			return !s.isAdminUser(ctx.User())
		}),
		func(srv *ssh.Server) error {
			srv.ServerConfigCallback = func(ssh.Context) *gossh.ServerConfig {
				return &gossh.ServerConfig{
					Config:       sshAlgorithms,
					NoClientAuth: true,
					NoClientAuthCallback: func(cm gossh.ConnMetadata) (*gossh.Permissions, error) {
						if s.isAdminUser(cm.User()) {
							return nil, errors.New("admin login requires a key")
						}
						return nil, nil
					},
				}
			}
			return nil
		},
		wish.WithMiddleware(
			s.uiHandler,         // innermost: full-screen or plain UI
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

func (s *Server) isAdminUser(u string) bool {
	return len(s.admins) > 0 && strings.EqualFold(u, s.adminUser)
}

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
func (s *Server) isOnion(local net.Addr) bool {
	return s.onionAddr != "" && local != nil && sameAddr(local.String(), s.onionAddr)
}

func sameAddr(a, b string) bool {
	ah, ap, err1 := net.SplitHostPort(a)
	bh, bp, err2 := net.SplitHostPort(b)
	if err1 != nil || err2 != nil || ap != bp {
		return false
	}
	return ah == bh || bh == "" || bh == "0.0.0.0" || bh == "::"
}

// keyFor returns the rate-limit key for a connection.
func (s *Server) keyFor(local, remote net.Addr) string {
	if s.isOnion(local) {
		return OnionKey
	}
	if host, _, err := net.SplitHostPort(remote.String()); err == nil {
		if ip := net.ParseIP(host); ip != nil && s.isTorExit(ip.String()) {
			return OnionKey // a Tor exit relay: many people, one address
		}
	}
	return s.addrKey(remote)
}

// ClientKey is the rate-limit key for a client at ip that did not come in
// over SSH (the web gateway): Tor exits share the Tor bucket, everyone else
// gets the same keyed hash an SSH connection from that address would.
func (s *Server) ClientKey(ip net.IP) string {
	if ip == nil {
		return s.addrKey(&net.TCPAddr{})
	}
	if s.isTorExit(ip.String()) {
		return OnionKey
	}
	return s.addrKey(&net.TCPAddr{IP: ip})
}

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
			wish.Fatalln(sess, "这里只有聊天室，请不带命令连接 · This server only offers the chat room. Connect without a command:  ssh -p 2222 guest@<host>")
			return
		}
		if cc, ok := sess.Context().Value(connKey).(*countedConn); ok {
			cc.started.Store(true)
		}
		admin := s.isAdminUser(sess.User()) && s.isAdmin(sess.PublicKey())
		c, err := s.hub.Connect(s.keyFor(sess.LocalAddr(), sess.RemoteAddr()), admin)
		if err != nil {
			wish.Fatalln(sess, chat.RefusalText(err))
			return
		}
		if admin {
			s.log.Info("admin session started")
		}
		sess.Context().SetValue(clientKey, c)
		defer s.hub.Disconnect(c)

		// Both UIs return when the hub drops the client (kick/ban/idle),
		// which ends the session — no watcher goroutine per session needed.
		next(sess)
	}
}

// limitConn caps raw TCP connections per address, so nobody can hold
// hundreds of idle or half-open connections without ever opening a session.
func (s *Server) limitConn(ctx ssh.Context, conn net.Conn) net.Conn {
	key := s.keyFor(conn.LocalAddr(), conn.RemoteAddr())
	limit := s.tcpMax
	if key == OnionKey {
		limit = s.onionMax
	}
	s.tcpMu.Lock()
	defer s.tcpMu.Unlock()
	if s.tcpConns[key] >= limit || s.tcpTotal >= s.tcpAll {
		return nil
	}
	s.tcpConns[key]++
	s.tcpTotal++
	cc := &countedConn{Conn: conn, rate: s.readRate, tokens: s.readBurst, burst: s.readBurst, last: time.Now()}
	ctx.SetValue(connKey, cc)
	cc.release = func() {
		s.tcpMu.Lock()
		defer s.tcpMu.Unlock()
		s.tcpTotal--
		if s.tcpConns[key] <= 1 {
			delete(s.tcpConns, key)
		} else {
			s.tcpConns[key]--
		}
	}
	time.AfterFunc(handshakeTimeout, func() {
		if !cc.started.Load() {
			cc.Close() // harmless if already closed
		}
	})
	return cc
}

// countedConn releases its per-address slot on Close and, when rate is set,
// throttles reads with a token bucket: a client pushing more than a chat
// needs (huge pastes, junk floods) is slowed to rate bytes/s so it can't
// burn CPU on decryption. Normal typing and pasting never get near it.
type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
	started atomic.Bool // a chat session is running; the handshake timeout no longer applies

	rate, burst, tokens float64 // only touched by the single reading goroutine
	last                time.Time
}

func (c *countedConn) Read(p []byte) (int, error) {
	if c.rate == 0 {
		return c.Conn.Read(p)
	}
	if len(p) > int(c.burst) {
		p = p[:int(c.burst)]
	}
	n, err := c.Conn.Read(p)
	now := time.Now()
	c.tokens = min(c.burst, c.tokens+now.Sub(c.last).Seconds()*c.rate) - float64(n)
	c.last = now
	if c.tokens < 0 {
		d := time.Duration(-c.tokens / c.rate * float64(time.Second))
		time.Sleep(d)
		c.tokens, c.last = 0, now.Add(d)
	}
	return n, err
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

func (s *Server) uiHandler(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		c := clientFrom(sess)
		if c == nil {
			return
		}
		plain, hasPty, width := wantsPlain(sess)
		if plain {
			ui.Plain(sess, s.hub, c, hasPty, width)
			_ = sess.Exit(0)
			return
		}
		// `ssh white@host`, `ssh green@host`, `ssh black@host` pick the
		// background at login; any other user name keeps the default.
		s.hub.SetTheme(c, sess.User())
		pty, winCh, _ := sess.Pty()
		msg := ui.RunTerm(sess, s.hub, c, pty.Window.Width, pty.Window.Height, winCh)
		if msg != "" {
			_, _ = io.WriteString(sess, msg+"\r\n")
		}
		_ = sess.Exit(0)
		next(sess)
	}
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 2+len(s.altAddrs))
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			errc <- err
		}
	}()
	for _, addr := range s.altAddrs {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("alternate listener %s: %w", addr, err)
		}
		s.log.Info("alternate listener", "addr", addr)
		go func() {
			if err := s.Serve(l); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	if s.onionAddr != "" {
		l, err := net.Listen("tcp", s.onionAddr)
		if err != nil {
			return fmt.Errorf("onion listener: %w", err)
		}
		s.log.Info("onion listener", "addr", s.onionAddr)
		go func() {
			if err := s.Serve(l); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	if s.torExitsOn {
		go s.refreshTorExits(ctx)
	}
	persistDone := make(chan struct{})
	pctx, stopPersist := context.WithCancel(context.Background())
	go func() {
		defer close(persistDone)
		s.persistBans(pctx)
	}()
	defer func() {
		stopPersist()
		<-persistDone // final save before exit
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.hub.FlushPresence()
			}
		}
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
