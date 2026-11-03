// Command ptterm runs Private-Talk Terminal: an anonymous chat room that
// anyone can join with nothing but an SSH client:
//
//	ssh -p 2222 guest@term.example.com
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
		msgRate     = flag.Float64("msg-rate", 1, "messages per second allowed (sustained)")
		msgBurst    = flag.Float64("msg-burst", 5, "message burst size")
		maxLen      = flag.Int("max-len", 500, "maximum message length in characters")
		idle        = flag.Duration("idle-timeout", 30*time.Minute, "disconnect idle sessions (0 = never)")
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	roomDefs, err := parseRooms(*rooms)
	if err != nil {
		log.Error("bad -rooms", "err", err)
		os.Exit(2)
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
	})

	srv, err := server.New(hub, server.Options{
		Addr:          *addr,
		HostKeyPath:   *hostKey,
		AdminKeysPath: *adminKeys,
		IdleTimeout:   *idle,
	}, log)
	if err != nil {
		log.Error("could not create server", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("Private-Talk Terminal listening", "addr", *addr, "version", version)
	if err := srv.Run(ctx); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
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
