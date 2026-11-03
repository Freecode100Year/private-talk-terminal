// Command ptterm runs Private-Talk Terminal: an anonymous chat room that
// anyone can join with nothing but an SSH client:
//
//	ssh -p 2222 guest@term.example.com
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // /tz works even on minimal systems without zoneinfo

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
	"github.com/Freecode100Year/private-talk-terminal/internal/server"
)

var version = "dev"

func main() {
	var (
		addr        = flag.String("addr", envOr("PTT_ADDR", ":2222"), "listen address")
		hostKey     = flag.String("host-key", envOr("PTT_HOST_KEY", "./data/host_ed25519"), "SSH host key path (created if missing)")
		adminKeys   = flag.String("admin-keys", envOr("PTT_ADMIN_KEYS", ""), "authorized_keys file whose keys get admin rights (optional)")
		rooms       = flag.String("rooms", envOr("PTT_ROOMS", ""), "public rooms, comma separated name[:topic] (first = lobby)")
		history     = flag.Int("history", 50, "messages kept per room")
		historyTTL  = flag.Duration("history-ttl", time.Hour, "discard messages older than this")
		maxClients  = flag.Int("max-clients", 1000, "maximum concurrent sessions")
		perIP       = flag.Int("max-per-ip", 3, "maximum concurrent sessions per address (IPv6 per /64)")
		connPerMin  = flag.Int("conn-per-min", 10, "new sessions per address per minute")
		tcpPerIP    = flag.Int("max-tcp-per-ip", 8, "raw TCP connections per address, including unauthenticated ones")
		adminUser   = flag.String("admin-user", envOr("PTT_ADMIN_USER", "admin"), "username admins log in with (their key is checked); everyone else never sends a key")
		onionAddr   = flag.String("onion-addr", envOr("PTT_ONION_ADDR", ""), "extra listener for a Tor onion service, e.g. 127.0.0.1:2224 (Tor users share one rate-limit bucket)")
		msgRate     = flag.Float64("msg-rate", 1, "messages per second allowed (sustained)")
		msgBurst    = flag.Float64("msg-burst", 5, "message burst size")
		maxLen      = flag.Int("max-len", 500, "maximum message length in characters")
		idle        = flag.Duration("idle-timeout", time.Hour, "disconnect sessions that haven't sent anything for this long (0 = never)")
		motd        = flag.String("motd", envOr("PTT_MOTD", ""), "file with extra welcome lines shown on connect (optional)")
		healthAddr  = flag.String("health-addr", envOr("PTT_HEALTH_ADDR", ""), "serve GET /healthz (counts only) on this address, e.g. 127.0.0.1:2223")
		verbose     = flag.Bool("v", false, "debug logging")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("ptterm", version)
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	// Everything — including libraries that log through slog.Default() —
	// goes through a handler that drops network addresses.
	log := slog.New(redactHandler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})})
	slog.SetDefault(log)

	roomDefs, err := parseRooms(*rooms)
	if err != nil {
		log.Error("bad -rooms", "err", err)
		os.Exit(2)
	}
	var motdLines []string
	if *motd != "" {
		data, err := os.ReadFile(*motd)
		if err != nil {
			log.Error("cannot read -motd", "err", err)
			os.Exit(2)
		}
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			motdLines = append(motdLines, chat.Sanitize(l, 200))
		}
	}

	hub := chat.NewHub(chat.Config{
		PublicRooms:   roomDefs,
		HistorySize:   *history,
		HistoryTTL:    *historyTTL,
		MaxMsgRunes:   *maxLen,
		MaxClients:    *maxClients,
		MaxConnsPerIP: *perIP,
		ConnPerMinute: *connPerMin,
		MsgPerSecond:  *msgRate,
		MsgBurst:      *msgBurst,
		IdleKick:      *idle,
		Motd:          motdLines,
	})

	srv, err := server.New(hub, server.Options{
		Addr:          *addr,
		HostKeyPath:   *hostKey,
		AdminKeysPath: *adminKeys,
		IdleTimeout:   connIdle(*idle),
		MaxTCPPerIP:   *tcpPerIP,
		AdminUser:     *adminUser,
		OnionAddr:     *onionAddr,
	}, log)
	if err != nil {
		log.Error("could not create server", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *healthAddr != "" {
		go serveHealth(ctx, *healthAddr, hub, log)
	}
	log.Info("Private-Talk Terminal listening", "addr", *addr, "version", version)
	if err := srv.Run(ctx); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// redactHandler removes attributes that could carry a client address
// (e.g. the SSH library logs "remote" on panics) before they reach the journal.
type redactHandler struct{ slog.Handler }

var redactKeys = map[string]bool{"remote": true, "remote_addr": true, "addr": true, "ip": true, "client": true, "peer": true, "user": true}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if redactKeys[strings.ToLower(a.Key)] && !strings.HasPrefix(r.Message, "Private-Talk Terminal listening") &&
			r.Message != "health endpoint" && r.Message != "onion listener" {
			a = slog.String(a.Key, "[redacted]")
		}
		clean.AddAttrs(a)
		return true
	})
	return h.Handler.Handle(ctx, clean)
}

func (h redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return redactHandler{h.Handler.WithAttrs(as)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h.Handler.WithGroup(name)}
}

// connIdle is the transport-level idle limit. The chat-level limit (based on
// what users send) is enforced by the hub; this one only catches silent
// connections, so it is a bit longer.
func connIdle(chatIdle time.Duration) time.Duration {
	if chatIdle <= 0 {
		return 0
	}
	return chatIdle + 5*time.Minute
}

func serveHealth(ctx context.Context, addr string, hub *chat.Hub, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Version string `json:"version"`
			chat.Stats
		}{"ok", version, hub.Stats()})
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Info("health endpoint", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("health endpoint failed", "err", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseRooms(s string) ([]chat.RoomDef, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []chat.RoomDef
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		name, topic, _ := strings.Cut(strings.TrimSpace(part), ":")
		name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "#"))
		if !chat.ValidRoomName(name) || strings.HasPrefix(name, "p-") {
			return nil, fmt.Errorf("invalid room name %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate room %q", name)
		}
		seen[name] = true
		out = append(out, chat.RoomDef{Name: name, Topic: strings.TrimSpace(topic)})
	}
	return out, nil
}
