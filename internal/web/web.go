// Package web is the browser entrance to VIVANT: a small static page plus a
// WebSocket endpoint that runs the same line-mode UI an `ssh plain@host`
// session gets. It exists for networks (and programs) that can speak HTTPS
// but not SSH.
//
// Protocol of /ws, for bots and scripts: every text message the server sends
// is a chunk of plain-text output ("\n"-separated lines, no ANSI, an
// occasional "\a" for the bell); every text message the client sends is one
// line of input — a chat message or a /command, exactly as typed over SSH.
//
// Privacy matches the SSH side: addresses are only turned into the same
// keyed hash used for rate limits and bans, never logged or stored. Nothing
// is set in the browser (no cookies, no storage) and the page loads no
// third-party resources.
package web

import (
	"context"
	"embed"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
	"github.com/Freecode100Year/vivant/internal/ui"
)

//go:embed static
var static embed.FS

// Options configures the web gateway.
type Options struct {
	Addr string
	// ProxyHeader names the header carrying the real client address when
	// the gateway sits behind a reverse proxy or Cloudflare (e.g.
	// "CF-Connecting-IP" or "X-Forwarded-For"). Leave empty when clients
	// connect directly: the header would otherwise be forgeable.
	ProxyHeader string
	// Key maps a client address to the hub's rate-limit key.
	Key func(ip net.IP) string
	// AllowOrigins lists extra browser origins (scheme://host[:port]) that
	// may open /ws. The page's own origin is always allowed, and so are
	// clients that send no Origin (scripts, bots).
	AllowOrigins []string
}

// Server is the HTTP side of the gateway.
type Server struct {
	hub  *chat.Hub
	opt  Options
	log  *slog.Logger
	http *http.Server
}

// pingEvery keeps idle connections alive through proxies (Cloudflare drops
// WebSockets silent for 100 s) and notices dead peers.
var pingEvery = 30 * time.Second

const maxInput = 16 << 10 // bytes per incoming message; the hub caps runes further

// New builds the gateway.
func New(hub *chat.Hub, opt Options, log *slog.Logger) *Server {
	s := &Server{hub: hub, opt: opt, log: log}
	s.http = &http.Server{
		Addr:              opt.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	return s
}

// Handler serves the page, its assets and /ws.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.serveWS)
	mux.HandleFunc("GET /{$}", s.asset("static/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", s.asset("static/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /style.css", s.asset("static/style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) asset(name, ctype string) http.HandlerFunc {
	data, err := static.ReadFile(name)
	if err != nil {
		panic(err) // embedded at build time; can't happen
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	}
}

// clientIP returns the address used for rate limiting.
func (s *Server) clientIP(r *http.Request) net.IP {
	if h := s.opt.ProxyHeader; h != "" {
		v := r.Header.Get(h)
		if i := strings.LastIndexByte(v, ','); i >= 0 {
			v = v[i+1:] // X-Forwarded-For: the entry added by the nearest proxy
		}
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// originOK refuses cross-site pages opening sessions in a visitor's name
// (which would use up that visitor's per-address quota).
func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, a := range s.opt.AllowOrigins {
		if strings.EqualFold(strings.TrimRight(a, "/"), o) {
			return true
		}
	}
	return false
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	key := s.opt.Key(s.clientIP(r))
	ws, err := upgrade(w, r, maxInput)
	if err != nil {
		return
	}
	ws.idle = 3 * pingEvery
	c, err := s.hub.Connect(key, false)
	if err != nil {
		_ = ws.WriteText(chat.RefusalText(err) + "\n")
		ws.Close(1008, "refused")
		return
	}
	defer s.hub.Disconnect(c)

	pr, pw := io.Pipe()
	done := make(chan struct{})
	defer close(done)
	go func() { // websocket → UI input
		defer pw.Close()
		for {
			msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if !strings.HasSuffix(msg, "\n") {
				msg += "\n"
			}
			if _, err := io.WriteString(pw, msg); err != nil {
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if ws.Ping() != nil {
					return
				}
			}
		}
	}()

	ui.Plain(wsRW{ws: ws, r: pr}, s.hub, c, false, 80)
	_ = pr.Close()
	ws.Close(1000, "bye")
}

// wsRW adapts the connection to what ui.Plain expects: each Write becomes
// one text message; reads come from the input pipe.
type wsRW struct {
	ws *wsConn
	r  io.Reader
}

func (rw wsRW) Read(p []byte) (int, error) { return rw.r.Read(p) }

func (rw wsRW) Write(p []byte) (int, error) {
	if err := rw.ws.WriteText(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	l, err := net.Listen("tcp", s.opt.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, l)
}

// Serve is Run on an existing listener.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sctx)
	}()
	s.log.Info("web gateway listening", "addr", l.Addr().String())
	if err := s.http.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
