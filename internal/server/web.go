package server

// The web entry: the same chat, rooms and people as the SSH side, opened in
// a browser. One page (embedded, no external resources) talks JSON over a
// WebSocket to the hub; nothing here stores messages or addresses.

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
	"github.com/Freecode100Year/vivant/internal/ui"
)

//go:embed web
var webFiles embed.FS

// Proxy modes for the web listener: whose word to take for the client address.
const (
	ProxyNone       = "none"       // the TCP peer is the client
	ProxyLocal      = "local"      // a proxy on this machine (Caddy, nginx): last X-Forwarded-For entry
	ProxyCloudflare = "cloudflare" // Cloudflare (orange cloud) or cloudflared: CF-Connecting-IP
)

// ParseProxyMode validates a -web-proxy value.
func ParseProxyMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "off", "direct":
		return ProxyNone, nil
	case "local", "caddy", "nginx", "x-forwarded-for":
		return ProxyLocal, nil
	case "cloudflare", "cf":
		return ProxyCloudflare, nil
	}
	return "", errors.New("-web-proxy must be none, local or cloudflare")
}

// Cloudflare's published edge ranges (https://www.cloudflare.com/ips/).
// CF-Connecting-IP is only believed from these (or from this machine, for
// cloudflared), so nobody can spoof an address by connecting directly.
var cloudflareNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	} {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP picks the address rate limits and bans apply to.
func clientIP(r *http.Request, mode string) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return nil
	}
	switch mode {
	case ProxyLocal:
		if peer.IsLoopback() {
			// The proxy appends the address it saw: the last entry is the
			// only one a client can't forge.
			if v := r.Header.Values("X-Forwarded-For"); len(v) > 0 {
				parts := strings.Split(v[len(v)-1], ",")
				if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
					return ip
				}
			}
		}
	case ProxyCloudflare:
		if peer.IsLoopback() || inNets(peer, cloudflareNets) {
			if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); ip != nil {
				return ip
			}
		}
	}
	return peer
}

// sameOrigin: a browser page from another site may not open a chat session
// in the visitor's name. Non-browser clients send no Origin; they are no
// different from an SSH client and are let in.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	o = strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
	return strings.EqualFold(o, r.Host)
}

var safeHost = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]{1,255}$`)

func (s *Server) webHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	connect := "'self'"
	if safeHost.MatchString(r.Host) { // older Safari doesn't count ws(s): as 'self'
		connect += " wss://" + r.Host + " ws://" + r.Host
	}
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src "+connect+
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

func (s *Server) webHandler() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(static)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.serveWS)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.webHeaders(w, r)
		http.ServeFileFS(w, r, static, "index.html")
	})
	for _, name := range []string{"app.js", "app.css", "icon.svg"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			s.webHeaders(w, r)
			files.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// Wire format. Client → server: {"t":"say","text":…}, {"t":"font","v":"+"},
// {"t":"theme","v":"dark"}. Server → client: "hello" (palettes), "ev" (one
// chat event), "st" (status for the header and room bar), "clear", "bye".
type webIn struct {
	T    string `json:"t"`
	Text string `json:"text,omitempty"`
	V    string `json:"v,omitempty"`
}

type webEvent struct {
	T       string   `json:"t"`
	ID      uint64   `json:"id,omitempty"`
	TS      int64    `json:"ts"`
	HM      string   `json:"hm,omitempty"` // time in the zone set with /tz
	Kind    string   `json:"kind"`
	Room    string   `json:"room,omitempty"`
	From    string   `json:"from,omitempty"`
	To      string   `json:"to,omitempty"`
	Color   int      `json:"c"`
	Text    string   `json:"text"`
	En      string   `json:"en,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	LinesEn []string `json:"linesEn,omitempty"`
	Hist    bool     `json:"hist,omitempty"`
	Self    bool     `json:"self,omitempty"`    // written by the viewer
	Mention bool     `json:"mention,omitempty"` // mentions the viewer
	Ring    bool     `json:"ring,omitempty"`    // worth a notification
}

type webRoom struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Users   int    `json:"users"`
	Current bool   `json:"cur,omitempty"`
	Private bool   `json:"priv,omitempty"`
	Unread  int    `json:"unread,omitempty"`
	Mention bool   `json:"at,omitempty"`
}

type webStatus struct {
	T         string    `json:"t"`
	Nick      string    `json:"nick"`
	Room      string    `json:"room"`
	RoomLabel string    `json:"roomLabel"`
	Topic     string    `json:"topic"`
	Online    int       `json:"online"`
	RoomUsers int       `json:"roomUsers"`
	Rooms     []webRoom `json:"rooms"`
	Uptime    string    `json:"uptime"`
	Muted     bool      `json:"muted,omitempty"`
	Bell      bool      `json:"bell"`
	Theme     string    `json:"theme"`
	Font      int       `json:"font"`
}

var kindNames = map[chat.Kind]string{
	chat.KindChat: "chat", chat.KindAction: "action", chat.KindSystem: "system", chat.KindDMIn: "dmin",
	chat.KindDMOut: "dmout", chat.KindInfo: "info", chat.KindError: "error", chat.KindNotice: "notice",
	chat.KindMention: "mention",
}

const (
	webMaxFrame    = 8 << 10 // a 500-character message is ~2 KB of JSON
	webFramesPer10 = 60      // all client frames, on top of the hub's message limits
)

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	if !isUpgrade(r) {
		http.Error(w, "websocket only", http.StatusUpgradeRequired)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ip := clientIP(r, s.webProxy)
	if ip == nil {
		http.Error(w, "bad address", http.StatusBadRequest)
		return
	}
	nc, br, err := wsAccept(w, r)
	if err != nil {
		return
	}
	defer nc.Close()
	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return writeFrame(nc, opText, b)
	}
	closeWith := func(code uint16, reason string) {
		_ = nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = writeFrame(nc, opClose, closePayload(code, reason))
	}

	c, err := s.hub.Connect(s.keyFor(nil, &net.TCPAddr{IP: ip}), false)
	if err != nil {
		msg := "Sorry: " + err.Error()
		if zh, ok := refusalZh[err]; ok {
			msg = "抱歉：" + zh + " · Sorry: " + err.Error()
		}
		_ = send(map[string]string{"t": "bye", "reason": msg})
		closeWith(1008, "refused")
		return
	}
	s.hub.SetWeb(c)
	defer s.hub.Disconnect(c)
	if err := send(map[string]any{"t": "hello", "palettes": ui.WebPalettes(), "version": s.version, "ssh": s.webSSH}); err != nil {
		return
	}

	// Reader: frames from the browser become hub input. It runs in its own
	// goroutine; this one owns all writes to the connection.
	in := make(chan wsFrame, 8)
	poke := make(chan struct{}, 1)
	quit := make(chan string, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		s.webReadLoop(c, nc, br, in, poke, quit)
	}()

	var last []byte
	pushStatus := func() error {
		b, err := json.Marshal(s.webStatus(c))
		if err != nil || string(b) == string(last) {
			return err
		}
		last = b
		_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return writeFrame(nc, opText, b)
	}
	if pushStatus() != nil {
		return
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	tick := time.NewTicker(5 * time.Second) // online counts, room bar
	defer tick.Stop()
	for {
		select {
		case <-readerDone:
			return
		case reason := <-quit:
			_ = send(map[string]string{"t": "bye", "reason": reason})
			closeWith(1000, "")
			return
		case <-c.Done:
			reason := c.Reason()
			if reason == "" {
				reason = "已断开 · Disconnected"
			}
			_ = send(map[string]string{"t": "bye", "reason": reason})
			closeWith(1000, "")
			return
		case m := <-in:
			_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if writeFrame(nc, m.op, m.payload) != nil {
				return
			}
		case ev := <-c.Out:
			if send(s.webEvent(c, ev)) != nil {
				return
			}
			// drain whatever else is queued before refreshing the status
			for more := true; more; {
				select {
				case ev := <-c.Out:
					if send(s.webEvent(c, ev)) != nil {
						return
					}
				default:
					more = false
				}
			}
			if pushStatus() != nil {
				return
			}
		case <-poke:
			if pushStatus() != nil {
				return
			}
		case <-tick.C:
			if pushStatus() != nil {
				return
			}
		case <-ping.C:
			_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if writeFrame(nc, opPing, nil) != nil {
				return
			}
		}
	}
}

// wsFrame is a frame the reader asks the writer to send (pong, "clear").
type wsFrame struct {
	op      byte
	payload []byte
}

func (s *Server) webReadLoop(c *chat.Client, nc net.Conn, br *bufio.Reader, out chan<- wsFrame, poke chan<- struct{}, quit chan<- string) {
	reply := func(op byte, p []byte) {
		select {
		case out <- wsFrame{op, p}:
		default:
		}
	}
	var msg []byte
	var msgOp byte
	var window int64
	var count int
	for {
		_ = nc.SetReadDeadline(time.Now().Add(75 * time.Second))
		fin, op, payload, err := readFrame(br, webMaxFrame)
		if err != nil {
			return
		}
		switch op {
		case opClose:
			return
		case opPing:
			reply(opPong, payload)
			continue
		case opPong:
			continue
		case opText:
			msg, msgOp = append(msg[:0], payload...), op
		case opCont:
			if msgOp == 0 {
				return
			}
			msg = append(msg, payload...)
		default: // binary or reserved
			return
		}
		if len(msg) > webMaxFrame {
			return
		}
		if !fin {
			continue
		}
		msgOp = 0
		if w := time.Now().Unix() / 10; w != window {
			window, count = w, 0
		}
		if count++; count > webFramesPer10 {
			return
		}
		var m webIn
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		switch m.T {
		case "say":
			res := s.hub.Handle(c, m.Text)
			if res.Quit {
				select {
				case quit <- "再见 · Bye":
				default:
				}
				return
			}
			if res.Clear {
				b, _ := json.Marshal(map[string]string{"t": "clear"})
				reply(opText, b)
			}
		case "font":
			s.hub.SetFont(c, m.V)
		case "theme":
			s.hub.SetTheme(c, m.V)
		default:
			continue
		}
		select {
		case poke <- struct{}{}:
		default:
		}
	}
}

func (s *Server) webEvent(c *chat.Client, ev *chat.Event) webEvent {
	self := s.hub.Nick(c)
	out := webEvent{T: "ev", ID: ev.ID, TS: ev.Time.UnixMilli(), Kind: kindNames[ev.Kind], Room: ev.Room,
		From: ev.From, To: ev.To, Text: ev.Text, En: ev.En, Lines: ev.Lines, LinesEn: ev.LinesEn, Hist: ev.History}
	if loc := s.hub.Loc(c); loc != nil && loc != time.Local {
		out.HM = ev.Time.In(loc).Format("15:04")
	}
	if ev.From != "" {
		out.Color = ui.NickColor(ev.From)
	}
	if ev.Kind == chat.KindChat || ev.Kind == chat.KindAction {
		out.Self = ev.From == self
		out.Mention = !out.Self && ui.Mentions(ev.Text, self)
	}
	out.Ring = ui.Rings(ev, self)
	return out
}

func (s *Server) webStatus(c *chat.Client) webStatus {
	st := s.hub.Status(c)
	out := webStatus{T: "st", Nick: st.Nick, Room: st.Room, RoomLabel: st.RoomLabel, Topic: st.Topic, Online: st.Online,
		RoomUsers: st.RoomUsers, Uptime: ui.Uptime(st.Uptime), Muted: st.Muted, Bell: st.Bell, Theme: st.Theme, Font: st.Font}
	for _, r := range st.Rooms {
		out.Rooms = append(out.Rooms, webRoom{Name: r.Name, Label: r.Label, Users: r.Users, Current: r.Current,
			Private: r.Private, Unread: r.Unread, Mention: r.Mentioned})
	}
	return out
}

// serveWeb runs the web listener until ctx is cancelled.
func (s *Server) serveWeb(ctx context.Context, errc chan<- error) {
	srv := &http.Server{
		Addr:              s.webAddr,
		Handler:           s.webHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	l, err := net.Listen("tcp", s.webAddr)
	if err != nil {
		errc <- errors.New("web listener " + s.webAddr + ": " + err.Error())
		return
	}
	s.log.Info("web listener", "addr", s.webAddr, "proxy", s.webProxy)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errc <- err
	}
}
