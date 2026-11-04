// Command vivant runs VIVANT: an anonymous chat room that
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

	"github.com/Freecode100Year/vivant/internal/chat"
	"github.com/Freecode100Year/vivant/internal/server"
)

var version = "dev"

func main() {
	var (
		addr        = flag.String("addr", envOr("ADDR", ":2222"), "监听地址 · listen address")
		hostKey     = flag.String("host-key", envOr("HOST_KEY", "./data/host_ed25519"), "SSH 主机密钥路径，不存在会自动生成 · SSH host key path (created if missing)")
		adminKeys   = flag.String("admin-keys", envOr("ADMIN_KEYS", ""), "管理员公钥文件（authorized_keys 格式，可选）· authorized_keys file whose keys get admin rights (optional)")
		rooms       = flag.String("rooms", envOr("ROOMS", ""), "公共房间，逗号分隔 name[:主题]，第一个是大厅 · public rooms, comma separated name[:topic] (first = lobby)")
		history     = flag.Int("history", 50, "每个房间保留的消息条数 · messages kept per room")
		historyTTL  = flag.Duration("history-ttl", time.Hour, "消息保留时长 · discard messages older than this")
		maxClients  = flag.Int("max-clients", 1000, "最大同时在线会话数 · maximum concurrent sessions")
		perIP       = flag.Int("max-per-ip", 3, "每个地址最多会话数（IPv6 按 /64）· maximum sessions per address (IPv6 per /64)")
		connPerMin  = flag.Int("conn-per-min", 10, "每个地址每分钟最多新会话 · new sessions per address per minute")
		readRate    = flag.Int("read-rate", 32<<10, "每个连接每秒最多接收字节数，超出会被放慢（0 = 不限）· bytes/s one connection may send before it is slowed down (0 = off)")
		tcpPerIP    = flag.Int("max-tcp-per-ip", 8, "每个地址最多 TCP 连接数（含未认证的）· raw TCP connections per address, including unauthenticated ones")
		adminUser   = flag.String("admin-user", envOr("ADMIN_USER", "admin"), "管理员登录用户名（会校验公钥），其他人从不发送公钥 · username admins log in with (key checked); everyone else never sends a key")
		altAddr     = flag.String("alt-addr", envOr("ALT_ADDR", ""), "额外监听地址，逗号分隔，例如 :443 · extra listen addresses, comma separated, e.g. :443")
		torExits    = flag.Bool("tor-exits", envOr("TOR_EXITS", "on") != "off", "下载 Tor 出口节点列表，让 Tor 用户共用宽松限额 · fetch Tor's exit list so Tor users share the generous Tor limits")
		quietAbove  = flag.Int("quiet-above", 20, "房间超过这个人数后，进出提示每 30 秒汇总一次（-1 = 不汇总）· above this many members, join/leave lines are summarised every 30s (-1 = never)")
		banFile     = flag.String("ban-file", envOr("BAN_FILE", ""), "把封禁保存到这个文件，重启后保留（可选，有隐私代价，见文档）· persist bans in this file (opt-in; privacy trade-off, see docs)")
		onionAddr   = flag.String("onion-addr", envOr("ONION_ADDR", ""), "给 Tor 洋葱服务用的本地监听地址，例如 127.0.0.1:2224 · extra listener for a Tor onion service, e.g. 127.0.0.1:2224")
		msgRate     = flag.Float64("msg-rate", 1, "每秒允许发送的消息数（持续）· messages per second allowed (sustained)")
		msgBurst    = flag.Float64("msg-burst", 5, "允许连续发送的消息数 · message burst size")
		maxLen      = flag.Int("max-len", 500, "单条消息最大字数 · maximum message length in characters")
		idle        = flag.Duration("idle-timeout", time.Hour, "多久不发言就断开（0 = 不断开）· disconnect sessions silent for this long (0 = never)")
		motd        = flag.String("motd", envOr("MOTD", ""), "进入时额外显示的公告文本文件（可选）· file with extra welcome lines shown on connect (optional)")
		healthAddr  = flag.String("health-addr", envOr("HEALTH_ADDR", ""), "在此地址提供 GET /healthz（只返回计数），例如 127.0.0.1:2223 · serve GET /healthz (counts only) here, e.g. 127.0.0.1:2223")
		verbose     = flag.Bool("v", false, "调试日志 · debug logging")
		showVersion = flag.Bool("version", false, "显示版本号后退出 · print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "VIVANT — 匿名 SSH 聊天室 · anonymous SSH chat room\n\n用法 · Usage: vivant [选项 · options]\n环境变量 · Environment: VIVANT_ADDR, VIVANT_ROOMS, … (旧名 · legacy PTT_*)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("vivant", version)
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
		PersistBans:   *banFile != "",
		TorEnabled:    *onionAddr != "",
		TorExits:      *torExits,
		QuietAbove:    *quietAbove,
	})

	srv, err := server.New(hub, server.Options{
		Addr:          *addr,
		HostKeyPath:   *hostKey,
		AdminKeysPath: *adminKeys,
		IdleTimeout:   connIdle(*idle),
		MaxTCPPerIP:   *tcpPerIP,
		MaxTCP:        2**maxClients + 500,
		ReadRate:      *readRate,
		ReadBurst:     8 * *readRate,
		AdminUser:     *adminUser,
		OnionAddr:     *onionAddr,
		BanFile:       *banFile,
		AltAddrs:      splitList(*altAddr),
		TorExits:      *torExits,
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
	log.Info("VIVANT listening", "addr", *addr, "version", version)
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
		if redactKeys[strings.ToLower(a.Key)] && !strings.HasPrefix(r.Message, "VIVANT listening") &&
			r.Message != "health endpoint" && r.Message != "onion listener" && r.Message != "alternate listener" {
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

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envOr reads VIVANT_<k>, falling back to the pre-rename PTT_<k>.
func envOr(k, def string) string {
	for _, p := range []string{"VIVANT_", "PTT_"} {
		if v := os.Getenv(p + k); v != "" {
			return v
		}
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
