// Package chat holds the in-memory chat state: rooms, anonymous identities,
// private messages, short-lived history and abuse protection. It knows
// nothing about SSH or terminals; front-ends talk to it through Connect,
// Handle, Status and the Client's event channel.
package chat

import (
	"crypto/rand"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// Kind classifies an Event so front-ends can style it.
type Kind int

const (
	KindChat    Kind = iota // <nick> text
	KindAction              // * nick does something
	KindSystem              // joins, leaves, nick changes
	KindDMIn                // private message received
	KindDMOut               // private message sent (echo to sender)
	KindInfo                // command output for this user only
	KindError               // command error for this user only
	KindNotice              // admin broadcast
	KindMention             // someone @mentioned you in a room you're not in
)

// Event is one line (or block) of output for a client.
type Event struct {
	ID    uint64 // non-zero for events broadcast to a room (same for every viewer)
	Time  time.Time
	Kind  Kind
	Room  string
	From  string
	To    string
	Text  string
	Lines []string // extra lines for multi-line info blocks
	// En / LinesEn: the English version of a server message. Text/Lines
	// hold Chinese. Each viewer's UI shows one language (/lang); user
	// messages have no En.
	En      string
	LinesEn []string
	History bool   // replayed from the room buffer on join
	sender  uint64 // client ID of the author (never shown), for /forget
}

// RoomDef describes a public room created at startup.
type RoomDef struct {
	Name    string
	Topic   string
	TopicEn string // optional English topic
}

// Config tunes the hub. Zero values are replaced with defaults in NewHub.
type Config struct {
	PublicRooms   []RoomDef
	HistorySize   int           // messages kept per room
	HistoryTTL    time.Duration // messages older than this are discarded
	MaxMsgRunes   int
	MaxClients    int
	MaxConnsPerIP int           // concurrent sessions per address (IPv6: per /64)
	ConnPerMinute int           // new sessions per address per minute
	MsgBurst      float64       // token bucket size for messages
	MsgPerSecond  float64       // token refill rate
	NickCooldown  time.Duration // between /nick changes
	MaxPrivate    int           // private rooms alive at once
	IdleKick      time.Duration // drop sessions that haven't sent anything for this long (0 = never)
	OnionMaxConns int           // concurrent sessions arriving via Tor (they share one key)
	OnionPerMin   int           // new Tor sessions per minute
	Motd          []string      // extra lines shown on connect
	// Facts about the deployment that /privacy reports to users.
	PersistBans bool // address-hash key stored on disk (-ban-file)
	TorEnabled  bool // an onion listener is configured
	TorExits    bool // Tor exit relays share the Tor bucket
	// QuietAbove: in rooms with more members than this, individual
	// join/leave lines are replaced by a periodic summary (FlushPresence).
	// 0 = default (20); negative = never collapse.
	QuietAbove int
	// Lang is the UI language for clients that don't announce one ("zh" or "en").
	Lang string
}

func (c *Config) defaults() {
	if len(c.PublicRooms) == 0 {
		c.PublicRooms = []RoomDef{
			{"general", "大厅 · 什么都可以聊", "Lobby · anything goes"},
			{"chinese", "中文频道", "Chinese channel"},
			{"english", "英文频道", "English channel"},
			{"technology", "编程、Linux、网络、安全", "Programming, Linux, networks, security"},
			{"anonymous", "随便聊", "Off-topic"},
		}
	}
	if c.HistorySize <= 0 {
		c.HistorySize = 50
	}
	if c.HistoryTTL <= 0 {
		c.HistoryTTL = time.Hour
	}
	if c.MaxMsgRunes <= 0 {
		c.MaxMsgRunes = 500
	}
	if c.MaxClients <= 0 {
		c.MaxClients = 1000
	}
	if c.MaxConnsPerIP <= 0 {
		c.MaxConnsPerIP = 3
	}
	if c.ConnPerMinute <= 0 {
		c.ConnPerMinute = 10
	}
	if c.MsgBurst <= 0 {
		c.MsgBurst = 5
	}
	if c.MsgPerSecond <= 0 {
		c.MsgPerSecond = 1
	}
	if c.NickCooldown <= 0 {
		c.NickCooldown = 20 * time.Second
	}
	if c.MaxPrivate <= 0 {
		c.MaxPrivate = 500
	}
	if c.Lang != "en" {
		c.Lang = "zh"
	}
	if c.QuietAbove == 0 {
		c.QuietAbove = 20
	}
	if c.OnionMaxConns <= 0 {
		c.OnionMaxConns = 300
	}
	if c.OnionPerMin <= 0 {
		c.OnionPerMin = 120
	}
}

// OnionKey is the address key shared by every session that arrives through
// the Tor onion listener.
const OnionKey = "onion"

var (
	ErrBanned       = errors.New("this address is temporarily banned")
	ErrFull         = errors.New("server is full, try again later")
	ErrTooManyConns = errors.New("too many sessions from your address")
	ErrConnRate     = errors.New("connecting too fast, wait a minute")
)

type room struct {
	name    string
	topic   string
	topicEn string
	private bool
	members map[uint64]*Client
	history []Event
	seq     uint64 // chat/action messages ever posted, for unread counts
	joins   int    // collapsed presence since the last FlushPresence
	leaves  int
}

// Client is one connected session.
type Client struct {
	ID    uint64
	Out   <-chan *Event   // events are shared between recipients: treat as read-only
	Done  <-chan struct{} // closed when the hub drops this client (kick/ban/disconnect)
	out   chan *Event
	done  chan struct{}
	once  sync.Once
	ipKey string
	admin bool

	// guarded by Hub.mu
	nick        string
	room        string
	lastDMFrom  string
	tokens      float64
	tokensAt    time.Time
	lastText    string
	repeat      int
	violations  int
	violationAt time.Time
	mutedUntil  time.Time
	nickAt      time.Time
	createdAt   time.Time
	reason      string
	lastActive  time.Time
	ignores     map[uint64]bool
	loc         *time.Location
	seen        map[string]uint64 // room -> room.seq when the client last left/joined it
	mentioned   map[string]bool   // rooms where the client was @mentioned while away
	noBell      bool              // /bell off
	lang        string            // "zh" or "en": which half of server messages the UI shows
}

// Reason is the kick/ban reason after Done is closed (empty for normal exit).
func (c *Client) Reason() string { return c.reason }

func (c *Client) close(reason string) {
	c.once.Do(func() {
		c.reason = reason
		close(c.done)
	})
}

// send never blocks: a client that stops reading loses events rather than
// stalling the whole room.
func (c *Client) send(ev *Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	select {
	case c.out <- ev:
	default:
	}
}

// Hub is the whole chat server state.
type Hub struct {
	cfg     Config
	started time.Time

	mu       sync.Mutex
	nextID   uint64
	clients  map[uint64]*Client
	byNick   map[string]*Client // lower-cased nick
	rooms    map[string]*room
	public   []string
	bans     map[string]time.Time
	perIP    map[string]int
	connLog  map[string][]time.Time
	reports  []string
	eventID  uint64
	banVer   uint64 // bumped on every ban change, for persistence
	shutdown bool
}

// NewHub builds a hub with the given config.
func NewHub(cfg Config) *Hub {
	cfg.defaults()
	h := &Hub{
		cfg:     cfg,
		started: time.Now(),
		clients: map[uint64]*Client{},
		byNick:  map[string]*Client{},
		rooms:   map[string]*room{},
		bans:    map[string]time.Time{},
		perIP:   map[string]int{},
		connLog: map[string][]time.Time{},
	}
	for _, r := range cfg.PublicRooms {
		h.rooms[r.Name] = &room{name: r.Name, topic: r.Topic, topicEn: r.TopicEn, members: map[uint64]*Client{}}
		h.public = append(h.public, r.Name)
	}
	return h
}

// Config returns the effective configuration.
func (h *Hub) Config() Config { return h.cfg }

// Connect registers a new anonymous session coming from ipKey (an opaque,
// already-hashed address key). The client starts in the first public room.
func (h *Hub) Connect(ipKey string, admin bool) (*Client, error) {
	return h.ConnectLang(ipKey, admin, "")
}

// ConnectLang is Connect with the client's preferred language ("zh", "en",
// or "" for the server default).
func (h *Hub) ConnectLang(ipKey string, admin bool, lang string) (*Client, error) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.shutdown {
		return nil, ErrFull
	}
	if until, ok := h.bans[ipKey]; ok {
		if now.Before(until) && !admin {
			return nil, ErrBanned
		}
		if !now.Before(until) {
			delete(h.bans, ipKey)
			h.banVer++
		}
	}
	if !admin {
		maxConns, perMin := h.cfg.MaxConnsPerIP, h.cfg.ConnPerMinute
		if ipKey == OnionKey {
			maxConns, perMin = h.cfg.OnionMaxConns, h.cfg.OnionPerMin
		}
		if len(h.clients) >= h.cfg.MaxClients {
			return nil, ErrFull
		}
		if h.perIP[ipKey] >= maxConns {
			return nil, ErrTooManyConns
		}
		log := pruneTimes(h.connLog[ipKey], now.Add(-time.Minute))
		if len(log) >= perMin {
			h.connLog[ipKey] = log
			return nil, ErrConnRate
		}
		h.connLog[ipKey] = append(log, now)
	}

	h.nextID++
	c := &Client{
		ID:         h.nextID,
		out:        make(chan *Event, 128),
		done:       make(chan struct{}),
		ipKey:      ipKey,
		admin:      admin,
		tokens:     h.cfg.MsgBurst,
		tokensAt:   now,
		createdAt:  now,
		lastActive: now,
		ignores:    map[uint64]bool{},
		loc:        time.Local,
		seen:       map[string]uint64{},
		lang:       normLang(lang, h.cfg.Lang),
		mentioned:  map[string]bool{},
	}
	for _, name := range h.public { // unread counts start from "now"
		c.seen[name] = h.rooms[name].seq
	}
	c.Out, c.Done = c.out, c.done
	c.nick = h.freeNickLocked()
	h.clients[c.ID] = c
	h.byNick[strings.ToLower(c.nick)] = c
	h.perIP[ipKey]++

	c.send(&Event{Kind: KindInfo,
		Text: fmt.Sprintf("欢迎，%s！直接打字就能聊天，/help 查看命令。", c.nick), Lines: []string{"服务器能看到聊天内容，别发能认出你的信息 · /privacy"},
		En: fmt.Sprintf("Welcome, %s! Just type to chat; /help lists commands.", c.nick), LinesEn: []string{"The server can read messages — don't post anything that identifies you · /privacy"},
	})
	if len(h.cfg.Motd) > 0 {
		c.send(&Event{Kind: KindNotice, Text: h.cfg.Motd[0], Lines: h.cfg.Motd[1:]})
	}
	h.joinLocked(c, h.public[0])
	return c, nil
}

// Disconnect removes a client (idempotent).
func (h *Hub) Disconnect(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(c, "", "left")
}

func (h *Hub) removeLocked(c *Client, reason, verb string) {
	if _, ok := h.clients[c.ID]; !ok {
		return
	}
	if r := h.rooms[c.room]; r != nil {
		delete(r.members, c.ID)
		h.presenceLocked(r, c.nick, verb, verb == "left" || verb == "timed out")
		h.gcRoomLocked(r)
	}
	delete(h.clients, c.ID)
	delete(h.byNick, strings.ToLower(c.nick))
	if h.perIP[c.ipKey] <= 1 {
		delete(h.perIP, c.ipKey)
	} else {
		h.perIP[c.ipKey]--
	}
	c.close(reason)
}

// Shutdown tells every client the server is going away and drops them.
func (h *Hub) Shutdown(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdown = true
	for _, c := range h.clients {
		c.send(&Event{Kind: KindNotice, Text: "服务器正在重启，请稍后重新连接。", En: msg})
	}
	for _, c := range h.clients {
		h.removeLocked(c, msg, "left")
	}
}

// Janitor prunes expired history, bans and rate-limit records. Call it
// periodically (e.g. every minute).
func (h *Hub) Janitor() {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rooms {
		r.history = h.pruneHistory(r.history, now)
	}
	for k, until := range h.bans {
		if now.After(until) {
			delete(h.bans, k)
			h.banVer++
		}
	}
	if h.cfg.IdleKick > 0 {
		for _, c := range h.clients {
			if !c.admin && now.Sub(c.lastActive) > h.cfg.IdleKick {
				h.removeLocked(c, c.L(fmt.Sprintf("%s 未发言，已断开", h.cfg.IdleKick), fmt.Sprintf("idle for %s", h.cfg.IdleKick)), "timed out")
			}
		}
	}
	for k, log := range h.connLog {
		log = pruneTimes(log, now.Add(-time.Minute))
		if len(log) == 0 {
			delete(h.connLog, k)
		} else {
			h.connLog[k] = log
		}
	}
}

// Result tells the front-end about UI-level effects of a command.
type Result struct {
	Quit  bool
	Clear bool
}

// Handle processes one line of user input (a message or a /command).
func (h *Hub) Handle(c *Client, line string) Result {
	line = strings.TrimSpace(line)
	if line == "" {
		return Result{}
	}
	h.mu.Lock()
	c.lastActive = time.Now()
	h.mu.Unlock()
	if strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//") {
		return h.command(c, line)
	}
	if strings.HasPrefix(line, "//") { // "//text" sends a literal "/text"
		line = line[1:]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sayLocked(c, line, KindChat)
	return Result{}
}

func (h *Hub) command(c *Client, line string) Result {
	cmd, rest, _ := strings.Cut(line[1:], " ")
	cmd = strings.ToLower(cmd)
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "quit", "exit", "q", "bye":
		return Result{Quit: true}
	case "clear", "cls":
		return Result{Clear: true}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c.ID]; !ok {
		return Result{Quit: true}
	}

	switch cmd {
	case "help", "h", "?":
		h.helpLocked(c, rest)
	case "rooms", "list":
		h.roomsLocked(c)
	case "join", "j":
		h.cmdJoinLocked(c, rest)
	case "create", "new":
		h.cmdCreateLocked(c)
	case "leave", "part", "lobby":
		if c.room == h.public[0] {
			h.errorf(c, "你已经在 #%s 了。", "You are already in #%s.", c.room)
		} else {
			h.joinLocked(c, h.public[0])
		}
	case "msg", "m", "w", "tell", "dm":
		target, text, _ := strings.Cut(rest, " ")
		h.dmLocked(c, strings.TrimPrefix(target, "@"), text)
	case "r", "reply":
		if c.lastDMFrom == "" {
			h.errorf(c, "还没有人私信你。", "Nobody has messaged you yet.")
		} else {
			h.dmLocked(c, c.lastDMFrom, rest)
		}
	case "nick", "name":
		h.nickLocked(c, rest)
	case "who", "users", "names":
		h.whoLocked(c)
	case "me":
		h.sayLocked(c, rest, KindAction)
	case "ignore", "block":
		h.ignoreLocked(c, rest, true)
	case "unignore", "unblock":
		h.ignoreLocked(c, rest, false)
	case "report":
		target, reason, _ := strings.Cut(rest, " ")
		h.reportLocked(c, strings.TrimPrefix(target, "@"), reason)
	case "lang", "language", "语言":
		h.langLocked(c, rest)
	case "bell":
		h.bellLocked(c, rest)
	case "privacy", "policy":
		h.privacyLocked(c)
	case "forget":
		h.forgetLocked(c)
	case "tz", "time":
		h.tzLocked(c, rest)
	case "whoami":
		h.infof(c, "你是 %s，在 %s。", "You are %s in %s.", c.nick, h.roomLabel(c.room))
	// --- admin ---
	case "kick", "mute", "unmute", "ban", "unbanall", "bans", "unban", "broadcast", "announce", "topic", "reports":
		if !c.admin {
			h.errorf(c, "没有 /%s 这个命令，输入 /help 查看。", "Unknown command /%s — try /help", cmd)
			return Result{}
		}
		h.adminLocked(c, cmd, rest)
	default:
		h.errorf(c, "没有 /%s 这个命令，输入 /help 查看。", "Unknown command /%s — try /help", cmd)
	}
	return Result{}
}

// helpLocked shows the handful of commands people actually need; everything
// else is one step away under "/help all", so newcomers aren't handed a manual.
func (h *Hub) helpLocked(c *Client, arg string) {
	if a := strings.ToLower(strings.TrimSpace(arg)); a != "all" && a != "more" && a != "全部" {
		c.send(&Event{Kind: KindInfo,
			Text: "常用命令（直接打字就是发言）", Lines: []string{
				"/rooms              有哪些房间",
				"/join <房间>        进入房间，如 /join chinese",
				"/msg <昵称> <内容>  私聊",
				"/nick <名字>        改昵称",
				"/who                谁在这里",
				"/quit               退出",
				"/help all           全部命令：私人房间、屏蔽、隐私、语言…",
			},
			En: "Everyday commands (just type to talk)", LinesEn: []string{
				"/rooms              list rooms",
				"/join <room>        join a room, e.g. /join english",
				"/msg <nick> <text>  private message",
				"/nick <name>        change your nickname",
				"/who                who is here",
				"/quit               leave",
				"/help all           everything else: private rooms, ignoring, privacy, language…",
			}})
		return
	}
	zh := []string{
		"── 聊天 ──",
		"/msg <昵称> <内容>   私聊；/r <内容> 回复上一个私信你的人",
		"/me <动作>           动作消息，如 /me 挥手",
		"/nick <名字>         改昵称（2–16 字，中文可以）",
		"//text               发送以 / 开头的文字",
		"── 房间 ──",
		"/rooms  /join <房间>  /leave   房间栏 +N 未读，@N 有人 @你",
		"/create              私人房间，凭房间码进入，没人时自动销毁",
		"/who                 当前房间的人",
		"── 隐私与安全 ──",
		"/privacy             本服务器存什么、不存什么",
		"/forget              从服务器删除你发过的消息",
		"/ignore <昵称>       屏蔽某人（/unignore 取消）",
		"/report <昵称> <原因> 举报给管理员",
		"── 其他 ──",
		"/lang zh|en          界面语言",
		"/tz <时区>           按你的时区显示时间，如 /tz +8",
		"/bell on|off         被 @ 或收到私聊时响铃（默认开）",
		"/clear  /quit",
	}
	en := []string{
		"── chat ──",
		"/msg <nick> <text>   private message; /r <text> replies to the last one",
		"/me <action>         action message, e.g. /me waves",
		"/nick <name>         change nickname (2–16 characters)",
		"//text               send text starting with /",
		"── rooms ──",
		"/rooms  /join <room>  /leave   rooms bar: +N unread, @N you were mentioned",
		"/create              private room, joined by code, gone when empty",
		"/who                 who is in this room",
		"── privacy & safety ──",
		"/privacy             what this server keeps and doesn't",
		"/forget              erase your messages from the server",
		"/ignore <nick>       hide someone (/unignore to undo)",
		"/report <nick> <why> report to moderators",
		"── other ──",
		"/lang zh|en          interface language",
		"/tz <zone>           show times in your timezone, e.g. /tz Europe/Berlin",
		"/bell on|off         bell on mentions and private messages (on by default)",
		"/clear  /quit",
	}
	if c.admin {
		zh = append(zh, "── 管理员 ──",
			"/kick <昵称> [原因]  /mute <昵称> [分钟]  /unmute <昵称>",
			"/ban <昵称> [分钟]   /bans  /unban <id>  /unbanall",
			"/broadcast <内容>    /topic <内容>  /reports")
		en = append(en, "── admin ──",
			"/kick <nick> [reason]  /mute <nick> [min]  /unmute <nick>",
			"/ban <nick> [min]      /bans  /unban <id>  /unbanall",
			"/broadcast <text>      /topic <text>  /reports")
	}
	c.send(&Event{Kind: KindInfo, Text: "全部命令", Lines: zh, En: "All commands", LinesEn: en})
}

func (h *Hub) roomsLocked(c *Client) {
	var lines, linesEn []string
	for _, name := range h.public {
		r := h.rooms[name]
		mark := "  "
		if c.room == name {
			mark = "▸ "
		}
		extra, extraEn := "", ""
		if n := r.seq - c.seen[name]; c.room != name && n > 0 {
			extra, extraEn = fmt.Sprintf("  （%d 条新消息", n), fmt.Sprintf("  (%d new", n)
			if c.mentioned[name] {
				extra, extraEn = extra+"，有人 @你", extraEn+", @you"
			}
			extra, extraEn = extra+"）", extraEn+")"
		}
		lines = append(lines, fmt.Sprintf("%s#%-12s %3d   %s%s", mark, name, len(r.members), r.topic, extra))
		linesEn = append(linesEn, fmt.Sprintf("%s#%-12s %3d   %s%s", mark, name, len(r.members), r.topicFor("en"), extraEn))
	}
	if r := h.rooms[c.room]; r != nil && r.private {
		lines = append(lines, fmt.Sprintf("▸ %s（私人房间）%d 人", h.roomLabel(c.room), len(r.members)))
		linesEn = append(linesEn, fmt.Sprintf("▸ %s (private) %d here", h.roomLabel(c.room), len(r.members)))
	}
	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("房间 · 共 %d 人在线", len(h.clients)), Lines: lines,
		En: fmt.Sprintf("Rooms · %d online", len(h.clients)), LinesEn: linesEn})
}

func (h *Hub) cmdJoinLocked(c *Client, name string) {
	name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "#~"))
	if name == "" {
		h.errorf(c, "用法：/join <房间>，例如 /join chinese", "Usage: /join <room>, e.g. /join english")
		return
	}
	r := h.rooms[name]
	if r == nil {
		h.errorf(c, "没有 %q 这个房间。/rooms 查看公共房间，/create 创建私人房间。", "No such room %q. /rooms lists public rooms; /create makes a private one.", name)
		return
	}
	if c.room == name {
		h.errorf(c, "你已经在 %s 了。", "You are already in %s.", h.roomLabel(name))
		return
	}
	h.joinLocked(c, name)
}

func (h *Hub) cmdCreateLocked(c *Client) {
	n := 0
	for _, r := range h.rooms {
		if r.private {
			n++
		}
	}
	if n >= h.cfg.MaxPrivate {
		h.errorf(c, "现在私人房间太多了，请稍后再试。", "Too many private rooms right now, try later.")
		return
	}
	if !h.takeToken(c, 3) {
		return
	}
	var code string
	for {
		code = "p-" + randomCode(10)
		if h.rooms[code] == nil {
			break
		}
	}
	h.rooms[code] = &room{name: code, private: true, members: map[uint64]*Client{}}
	h.joinLocked(c, code)
	h.infof(c, "私人房间已创建。把这个房间码发给朋友：/join %s", "Private room created. Give your friends this code: /join %s", code)
	h.infof(c, "最后一个人离开后，房间连同聊天记录一起消失。", "The room disappears (with its history) when the last person leaves.")
}

func (h *Hub) joinLocked(c *Client, name string) {
	if old := h.rooms[c.room]; old != nil {
		c.seen[old.name] = old.seq
		delete(old.members, c.ID)
		h.presenceLocked(old, c.nick, "left for another room", true)
		h.gcRoomLocked(old)
	}
	r := h.rooms[name]
	c.room = name
	c.seen[name] = r.seq
	delete(c.mentioned, name)
	r.history = h.pruneHistory(r.history, time.Now())
	for _, ev := range r.history {
		e := ev
		e.History = true
		c.send(&e)
	}
	h.presenceLocked(r, c.nick, "joined", true) // tell the others; the joiner gets "Now in …"
	r.members[c.ID] = c
	topic, topicEn := r.topic, r.topicFor("en")
	if r.private {
		topic, topicEn = "私人房间", "private room"
	}
	c.send(&Event{Kind: KindInfo, Room: name,
		Text: fmt.Sprintf("已进入 %s · %s · %d 人", h.roomLabel(name), topic, len(r.members)),
		En:   fmt.Sprintf("Now in %s · %s · %d here", h.roomLabel(name), topicEn, len(r.members))})
}

func (h *Hub) gcRoomLocked(r *room) {
	if r.private && len(r.members) == 0 {
		delete(h.rooms, r.name)
	}
}

func (h *Hub) roomLabel(name string) string {
	if r := h.rooms[name]; r != nil && r.private {
		return "🔒" + name
	}
	return "#" + name
}

func (h *Hub) sayLocked(c *Client, text string, kind Kind) {
	if _, ok := h.clients[c.ID]; !ok {
		return
	}
	text = Sanitize(text, h.cfg.MaxMsgRunes)
	if text == "" {
		if kind == KindAction {
			h.errorf(c, "用法：/me <动作>", "Usage: /me <action>")
		}
		return
	}
	if h.mutedLocked(c) || !h.takeToken(c, 1) || h.repeatLocked(c, text) {
		return
	}
	r := h.rooms[c.room]
	r.seq++
	h.eventID++
	ev := &Event{ID: h.eventID, Time: time.Now(), Kind: kind, Room: r.name, From: c.nick, Text: text, sender: c.ID}
	r.history = append(r.history, *ev)
	if len(r.history) > h.cfg.HistorySize {
		r.history = append(r.history[:0:0], r.history[len(r.history)-h.cfg.HistorySize:]...)
	}
	for _, m := range r.members {
		if !m.ignores[c.ID] {
			m.send(ev)
		}
	}
	if !r.private { // never reveal a private room to someone outside it
		h.notifyMentionsLocked(c, r, ev)
	}
}

// notifyMentionsLocked tells people @mentioned in a room they aren't in. It
// only looks up the @words in the text, so it costs nothing for messages
// without mentions regardless of how many users are online.
func (h *Hub) notifyMentionsLocked(c *Client, r *room, ev *Event) {
	notified := map[uint64]bool{}
	rest := ev.Text
	for len(notified) < 5 {
		i := strings.IndexByte(rest, '@')
		if i < 0 {
			break
		}
		rest = rest[i+1:]
		// Chinese text often has no space after a name ("@夜猫子你好"), so
		// take the longest prefix (max 16 runes) that is an online nick.
		var cand []rune
		for _, ru := range rest {
			if len(cand) == 16 || ru == '@' || unicode.IsSpace(ru) || unicode.IsPunct(ru) && ru != '_' && ru != '-' {
				break
			}
			cand = append(cand, ru)
		}
		var t *Client
		for n := len(cand); n >= 2 && t == nil; n-- {
			t = h.byNick[strings.ToLower(string(cand[:n]))]
		}
		if t == nil || t == c || notified[t.ID] || r.members[t.ID] != nil || t.ignores[c.ID] {
			continue
		}
		notified[t.ID] = true
		t.mentioned[r.name] = true
		t.send(&Event{Time: ev.Time, Kind: KindMention, Room: r.name, From: c.nick, Text: ev.Text})
	}
}

func (h *Hub) dmLocked(c *Client, target, text string) {
	text = Sanitize(text, h.cfg.MaxMsgRunes)
	if target == "" || text == "" {
		h.errorf(c, "用法：/msg <昵称> <内容>", "Usage: /msg <nick> <message>")
		return
	}
	to := h.byNick[strings.ToLower(target)]
	if to == nil {
		h.errorf(c, "%q 不在线。", "No user named %q online.", target)
		return
	}
	if to == c {
		h.errorf(c, "在跟自己说话？😄", "Talking to yourself? 😄")
		return
	}
	if h.mutedLocked(c) || !h.takeToken(c, 1) || h.repeatLocked(c, text) {
		return
	}
	now := time.Now()
	if !to.ignores[c.ID] { // ignored senders aren't told, so ignoring can't be probed
		to.lastDMFrom = c.nick
		to.send(&Event{Time: now, Kind: KindDMIn, From: c.nick, To: to.nick, Text: text})
	}
	c.send(&Event{Time: now, Kind: KindDMOut, From: c.nick, To: to.nick, Text: text})
}

func (h *Hub) nickLocked(c *Client, n string) {
	n = Sanitize(n, 32)
	if n == "" {
		h.infof(c, "你现在叫 %s。用法：/nick <新名字>", "You are %s. Usage: /nick <new name>", c.nick)
		return
	}
	if !ValidNick(n) {
		h.errorf(c, "昵称要 2–16 个字，可用中文、字母、数字、_ 和 -。", "Nicknames are 2–16 letters, digits, _ or -.")
		return
	}
	if reservedNick(n) && !c.admin {
		h.errorf(c, "%q 是保留名字，换一个吧。", "%q is reserved.", n)
		return
	}
	if n == c.nick {
		return
	}
	if o := h.byNick[strings.ToLower(n)]; o != nil && o != c {
		h.errorf(c, "%q 已经有人用了。", "%q is taken.", n)
		return
	}
	if h.mutedLocked(c) {
		return
	}
	if wait := c.nickAt.Add(h.cfg.NickCooldown).Sub(time.Now()); wait > 0 && !c.admin {
		h.errorf(c, "请过 %d 秒再改名。", "Wait %ds before changing nick again.", int(wait.Seconds())+1)
		return
	}
	old := c.nick
	delete(h.byNick, strings.ToLower(old))
	c.nick = n
	c.nickAt = time.Now()
	h.byNick[strings.ToLower(n)] = c
	for _, o := range h.clients { // keep /r working across renames
		if o.lastDMFrom == old {
			o.lastDMFrom = n
		}
	}
	h.roomSystemLocked(h.rooms[c.room], fmt.Sprintf("%s 改名为 %s", old, n), fmt.Sprintf("%s is now known as %s", old, n))
}

func (h *Hub) whoLocked(c *Client) {
	r := h.rooms[c.room]
	names := make([]string, 0, len(r.members))
	for _, m := range r.members {
		names = append(names, m.nick)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	more := 0
	if len(names) > 120 {
		more = len(names) - 120
		names = names[:120]
	}
	var lines []string
	for i := 0; i < len(names); i += 6 {
		end := min(i+6, len(names))
		lines = append(lines, "  "+strings.Join(names[i:end], "  "))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("  … +%d", more))
	}
	c.send(&Event{Kind: KindInfo, Lines: lines, LinesEn: lines,
		Text: fmt.Sprintf("%s 里有 %d 人 · 共 %d 人在线", h.roomLabel(c.room), len(r.members), len(h.clients)),
		En:   fmt.Sprintf("%d in %s · %d online total", len(r.members), h.roomLabel(c.room), len(h.clients))})
}

func (h *Hub) adminLocked(c *Client, cmd, rest string) {
	arg, tail, _ := strings.Cut(rest, " ")
	tail = strings.TrimSpace(tail)
	switch cmd {
	case "broadcast", "announce":
		text := Sanitize(rest, h.cfg.MaxMsgRunes)
		if text == "" {
			h.errorf(c, "用法：/broadcast <内容>", "Usage: /broadcast <text>")
			return
		}
		h.eventID++
		ev := &Event{ID: h.eventID, Time: time.Now(), Kind: KindNotice, From: c.nick, Text: text}
		for _, o := range h.clients {
			o.send(ev)
		}
		return
	case "unbanall":
		n := len(h.bans)
		h.bans = map[string]time.Time{}
		h.banVer++
		h.infof(c, "已清除 %d 条封禁。", "Cleared %d bans.", n)
		return
	case "bans":
		if len(h.bans) == 0 {
			h.infof(c, "当前没有封禁。", "No active bans.")
			return
		}
		keys := make([]string, 0, len(h.bans))
		for k := range h.bans {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return h.bans[keys[i]].Before(h.bans[keys[j]]) })
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("  %s   %s", banID(k), time.Until(h.bans[k]).Round(time.Minute)))
		}
		c.send(&Event{Kind: KindInfo, Lines: lines, LinesEn: lines,
			Text: fmt.Sprintf("%d 条封禁（id 是匿名地址哈希，后面是剩余时间）· /unban <id>", len(keys)),
			En:   fmt.Sprintf("%d active bans (id = anonymous address hash, then time left) · /unban <id>", len(keys))})
		return
	case "unban":
		id := strings.ToLower(strings.TrimSpace(rest))
		for k := range h.bans {
			if id != "" && banID(k) == id {
				delete(h.bans, k)
				h.banVer++
				h.infof(c, "已解封 %s。", "Unbanned %s.", id)
				return
			}
		}
		h.errorf(c, "没有 id 为 %q 的封禁，见 /bans", "No ban with id %q — see /bans", id)
		return
	case "reports":
		if len(h.reports) == 0 {
			h.infof(c, "暂无举报。", "No reports.")
			return
		}
		c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("最近 %d 条举报", len(h.reports)), En: fmt.Sprintf("%d recent reports", len(h.reports)),
			Lines: append([]string(nil), h.reports...), LinesEn: append([]string(nil), h.reports...)})
		return
	case "topic":
		r := h.rooms[c.room]
		r.topic = Sanitize(rest, 80)
		r.topicEn = r.topic
		h.roomSystemLocked(r, "房间主题改为："+r.topic, "Topic set to: "+r.topic)
		return
	}
	t := h.byNick[strings.ToLower(strings.TrimPrefix(arg, "@"))]
	if t == nil {
		h.errorf(c, "用法：/%s <昵称> …（找不到这个人）", "Usage: /%s <nick> … (user not found)", cmd)
		return
	}
	if t.admin && t != c {
		h.errorf(c, "不能对另一位管理员用 /%s。", "Cannot /%s another admin.", cmd)
		return
	}
	minutes := 0
	fmt.Sscanf(tail, "%d", &minutes)
	switch cmd {
	case "kick":
		reason := t.L("被管理员踢出", "kicked by admin")
		if tail != "" {
			reason = t.L("被踢出：", "kicked: ") + Sanitize(tail, 120)
		}
		h.removeLocked(t, reason, "was kicked")
	case "mute":
		if minutes <= 0 {
			minutes = 10
		}
		t.mutedUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		t.send(&Event{Kind: KindError, Text: fmt.Sprintf("你被管理员禁言 %d 分钟。", minutes), En: fmt.Sprintf("You were muted for %d min by an admin.", minutes)})
		h.infof(c, "已禁言 %s %d 分钟。", "Muted %s for %d min.", t.nick, minutes)
	case "unmute":
		t.mutedUntil = time.Time{}
		t.violations = 0
		t.send(&Event{Kind: KindInfo, Text: "你已解除禁言。", En: "You can talk again."})
		h.infof(c, "已解除 %s 的禁言。", "Unmuted %s.", t.nick)
	case "ban":
		if t.ipKey == OnionKey { // banning the shared Tor key would lock out every Tor user
			h.removeLocked(t, t.L("被管理员踢出", "kicked by admin"), "was kicked")
			h.infof(c, "%s 通过 Tor 连接，无法按地址封禁，已改为踢出。屡犯可用 /mute。", "%s came via Tor and can't be banned by address — kicked instead. Use /mute for repeat offenders.", arg)
			return
		}
		if minutes <= 0 {
			minutes = 60
		}
		h.bans[t.ipKey] = time.Now().Add(time.Duration(minutes) * time.Minute)
		h.banVer++
		key := t.ipKey
		for _, o := range h.clients {
			if o.ipKey == key && !o.admin {
				h.removeLocked(o, o.L(fmt.Sprintf("被封禁 %d 分钟", minutes), fmt.Sprintf("banned for %d min", minutes)), "was banned")
			}
		}
		h.infof(c, "已封禁 %s 的地址 %d 分钟。", "Banned %s's address for %d min.", arg, minutes)
	}
}

func (h *Hub) ignoreLocked(c *Client, nick string, on bool) {
	nick = strings.TrimPrefix(strings.TrimSpace(nick), "@")
	if nick == "" {
		var names []string
		for id := range c.ignores {
			if o := h.clients[id]; o != nil {
				names = append(names, o.nick)
			}
		}
		if len(names) == 0 {
			h.infof(c, "你没有屏蔽任何人。用法：/ignore <昵称>", "You aren't ignoring anyone. Usage: /ignore <nick>")
		} else {
			sort.Strings(names)
			h.infof(c, "已屏蔽：%s", "Ignoring: %s", strings.Join(names, ", "))
		}
		return
	}
	t := h.byNick[strings.ToLower(nick)]
	if t == nil {
		h.errorf(c, "%q 不在线。", "No user named %q online.", nick)
		return
	}
	if t == c {
		h.errorf(c, "不能屏蔽自己。", "You can't ignore yourself.")
		return
	}
	if on {
		c.ignores[t.ID] = true
		h.infof(c, "已屏蔽 %s，看不到对方的消息（对方重连后失效）。", "Ignoring %s — you won't see their messages (until they reconnect).", t.nick)
	} else {
		delete(c.ignores, t.ID)
		h.infof(c, "已取消屏蔽 %s。", "No longer ignoring %s.", t.nick)
	}
}

func (h *Hub) reportLocked(c *Client, nick, reason string) {
	reason = Sanitize(reason, 200)
	t := h.byNick[strings.ToLower(nick)]
	if nick == "" || t == nil {
		h.errorf(c, "用法：/report <昵称> <原因>（对方须在线）", "Usage: /report <nick> <reason> (user must be online)")
		return
	}
	if !h.takeToken(c, 3) {
		return
	}
	line := fmt.Sprintf("[%s] %s reported %s in %s: %s", time.Now().UTC().Format("01-02 15:04Z"), c.nick, t.nick, h.roomLabel(t.room), reason)
	h.reports = append(h.reports, line)
	if len(h.reports) > 50 {
		h.reports = h.reports[len(h.reports)-50:]
	}
	admins := 0
	for _, o := range h.clients {
		if o.admin {
			admins++
			o.send(&Event{Kind: KindNotice, Text: "举报", En: "Report", Lines: []string{line}, LinesEn: []string{line}})
		}
	}
	if admins > 0 {
		h.infof(c, "已通知 %d 位管理员。", "Report sent to %d moderator(s).", admins)
	} else {
		h.infof(c, "已记录，管理员上线后可见。", "Report saved; no moderator is online right now.")
	}
}

// ParseZone accepts IANA names (Asia/Shanghai), "utc"/"local", and
// fixed offsets like +8, -5, +05:30.
func ParseZone(s string) (*time.Location, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if v, ok := zoneCache.Load(key); ok {
		return v.(*time.Location), nil
	}
	loc, err := parseZone(s)
	if err == nil {
		if n := zoneCount.Add(1); n <= 2000 { // bounded: zones are interned so renders can be shared
			zoneCache.Store(key, loc)
		}
	}
	return loc, err
}

var (
	zoneCache sync.Map
	zoneCount atomic.Int64
)

func parseZone(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "utc", "gmt", "z":
		return time.UTC, nil
	case "local", "server":
		return time.Local, nil
	}
	if m := tzOffset.FindStringSubmatch(s); m != nil {
		hours, _ := strconv.Atoi(m[2])
		mins := 0
		if m[3] != "" {
			mins, _ = strconv.Atoi(m[3])
		}
		if hours > 14 || mins > 59 {
			return nil, fmt.Errorf("offset out of range")
		}
		sign := m[1]
		if sign == "" {
			sign = "+"
		}
		off := hours*3600 + mins*60
		if sign == "-" {
			off = -off
		}
		name := fmt.Sprintf("UTC%s%d", sign, hours)
		if mins > 0 {
			name += fmt.Sprintf(":%02d", mins)
		}
		return time.FixedZone(name, off), nil
	}
	if strings.Contains(s, "..") || len(s) > 40 {
		return nil, fmt.Errorf("bad zone")
	}
	return time.LoadLocation(s)
}

var tzOffset = regexp.MustCompile(`^(?:UTC|GMT)?([+-]?)(\d{1,2})(?::?(\d{2}))?$`)

func (h *Hub) tzLocked(c *Client, arg string) {
	if strings.TrimSpace(arg) == "" {
		h.infof(c, "时间按 %s 显示（现在 %s）。用法：/tz Asia/Shanghai、/tz +8、/tz utc", "Times are shown in %s (now %s). Usage: /tz Europe/Berlin | /tz America/New_York | /tz -5 | /tz utc",
			c.loc.String(), time.Now().In(c.loc).Format("15:04"))
		return
	}
	loc, err := ParseZone(arg)
	if err != nil {
		h.errorf(c, "不认识时区 %q，例如 /tz Asia/Shanghai 或 /tz +8", "Unknown timezone %q — e.g. /tz Europe/Berlin or /tz -5", Sanitize(arg, 40))
		return
	}
	c.loc = loc
	h.infof(c, "时区已设为 %s，现在 %s。", "Timezone set to %s — now %s.", loc.String(), time.Now().In(loc).Format("15:04"))
}

// presenceLocked announces a join/leave. Busy rooms get a periodic summary
// instead, so arrivals and departures don't drown the conversation. Kicks
// and bans are never collapsed: moderation stays visible.
var presenceText = map[string][2]string{
	"joined":                {"%s 进入了房间", "%s joined"},
	"left":                  {"%s 离开了", "%s left"},
	"left for another room": {"%s 去了别的房间", "%s left for another room"},
	"timed out":             {"%s 长时间未发言，已断开", "%s timed out"},
	"was kicked":            {"%s 被管理员踢出", "%s was kicked"},
	"was banned":            {"%s 被管理员封禁", "%s was banned"},
}

func (h *Hub) presenceLocked(r *room, nick, verb string, collapsible bool) {
	if r == nil {
		return
	}
	if collapsible && h.cfg.QuietAbove > 0 && len(r.members) > h.cfg.QuietAbove {
		if verb == "joined" {
			r.joins++
		} else {
			r.leaves++
		}
		return
	}
	t, ok := presenceText[verb]
	if !ok {
		t = [2]string{"%s " + verb, "%s " + verb}
	}
	h.roomSystemLocked(r, fmt.Sprintf(t[0], nick), fmt.Sprintf(t[1], nick))
}

// FlushPresence posts the collapsed join/leave summary of busy rooms. Call it
// periodically (e.g. every 30 seconds).
func (h *Hub) FlushPresence() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rooms {
		if r.joins == 0 && r.leaves == 0 {
			continue
		}
		var zh, en []string
		if r.joins > 0 {
			zh = append(zh, fmt.Sprintf("%d 人进入", r.joins))
			en = append(en, fmt.Sprintf("%d joined", r.joins))
		}
		if r.leaves > 0 {
			zh = append(zh, fmt.Sprintf("%d 人离开", r.leaves))
			en = append(en, fmt.Sprintf("%d left", r.leaves))
		}
		h.roomSystemLocked(r, strings.Join(zh, "、"), strings.Join(en, ", "))
		r.joins, r.leaves = 0, 0
	}
}

func (h *Hub) bellLocked(c *Client, arg string) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "off", "0", "关":
		c.noBell = true
	case "on", "1", "开":
		c.noBell = false
	case "":
		c.noBell = !c.noBell
	default:
		h.errorf(c, "用法：/bell on 或 /bell off", "Usage: /bell on or /bell off")
		return
	}
	if c.noBell {
		h.infof(c, "响铃已关闭。", "Bell off.")
	} else {
		h.infof(c, "被 @ 或收到私聊时会响铃。", "Bell on: you'll hear it when mentioned or messaged.")
	}
}

// BellOn reports whether the client wants a bell on mentions and DMs.
func (h *Hub) BellOn(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !c.noBell
}

// privacyLocked tells the user, from the live configuration, what this
// server keeps about them and what it can't promise.
func (h *Hub) privacyLocked(c *Client) {
	bans, bansEn := "仅在内存，重启即清空", "memory only, gone on restart"
	if h.cfg.PersistBans {
		bans, bansEn = "已开启持久化：仅被封禁地址的哈希写入磁盘", "persisted: only hashes of banned addresses are written to disk"
	}
	tor, torEn := "未开启洋葱服务", "no onion service on this server"
	if h.cfg.TorEnabled {
		tor, torEn = "已开启，可通过 .onion 地址连接，服务器看不到你的 IP", "onion service available: connect via .onion and the server never sees your IP"
	}
	if h.cfg.TorExits {
		tor += "；用 Tor 浏览器直连也欢迎，Tor 用户共用一组宽松限额"
		torEn += "; Tor Browser users are welcome and share generous limits"
	}
	idle, idleEn := "不限", "none"
	if h.cfg.IdleKick > 0 {
		idle, idleEn = h.cfg.IdleKick.String(), h.cfg.IdleKick.String()
	}
	zh := []string{
		fmt.Sprintf("消息   只在内存里，每个房间最多 %d 条、最长 %s，然后自动删除；私聊不保存", h.cfg.HistorySize, h.cfg.HistoryTTL),
		"不记录 IP、昵称、消息内容都不写入日志；不收集 SSH 公钥；没有账号",
		"地址   只用来限流和封禁，以匿名哈希保存，不显示给任何人（包括管理员）",
		"封禁   " + bans,
		"Tor    " + tor,
		"空闲   " + idle + " 不发言自动断开",
		"管理员 可以踢出、禁言、封禁（都会公开显示）；看不到私聊，聊天室里也没有查看 IP 的功能",
		"删除   /forget 立刻从房间历史中删除你本次连接发过的所有消息",
		"边界   SSH 只加密传输：服务器进程能看到明文，VPS 运营者在系统层面能看到连接来源 IP。",
		"       想对运营者也隐藏 IP 请用 Tor；任何时候都不要发送能识别你身份的信息。",
	}
	en := []string{
		fmt.Sprintf("Messages   memory only, at most %d per room for %s, then deleted; private messages are never stored", h.cfg.HistorySize, h.cfg.HistoryTTL),
		"No logs    no IPs, nicknames or message text in logs; no SSH public keys collected; no accounts",
		"Addresses  used only for rate limits and bans, kept as anonymous hashes, shown to no one (admins included)",
		"Bans       " + bansEn,
		"Tor        " + torEn,
		"Idle       disconnected after " + idleEn + " without posting",
		"Admins     can kick, mute and ban (always shown publicly); can't read private messages; no IP lookup in the chat",
		"Erase      /forget removes everything you posted this session from room history, now",
		"Limits     SSH encrypts the connection only: the server process sees plaintext and the VPS operator sees source IPs.",
		"           Use Tor to hide your IP from the operator too; never post anything that identifies you.",
	}
	c.send(&Event{Kind: KindInfo, Text: "本服务器的数据政策", Lines: zh, En: "Privacy on this server", LinesEn: en})
}

// forgetLocked removes every message this session posted from all room
// histories. Lines already on other people's screens can't be recalled.
func (h *Hub) forgetLocked(c *Client) {
	if !h.takeToken(c, 1) {
		return
	}
	n := 0
	for _, r := range h.rooms {
		kept := r.history[:0]
		for _, ev := range r.history {
			if ev.sender == c.ID {
				n++
				continue
			}
			kept = append(kept, ev)
		}
		// clear the tail so removed texts don't linger in the backing array
		for i := len(kept); i < len(r.history); i++ {
			r.history[i] = Event{}
		}
		r.history = kept
	}
	h.infof(c, "已从房间历史删除你的 %d 条消息；已经显示在别人屏幕上的无法撤回。", "Erased %d of your messages from room history; lines already on others' screens can't be recalled.", n)
}

// --- abuse protection -------------------------------------------------------

func (h *Hub) mutedLocked(c *Client) bool {
	if c.mutedUntil.IsZero() || time.Now().After(c.mutedUntil) {
		return false
	}
	left := time.Until(c.mutedUntil).Round(time.Second)
	h.errorf(c, "你正在禁言中，还剩 %s。", "You are muted for %s more.", left)
	return true
}

// takeToken charges cost tokens from the client's bucket. Repeated limit hits
// within a minute escalate to an automatic short mute.
func (h *Hub) takeToken(c *Client, cost float64) bool {
	if c.admin {
		return true
	}
	now := time.Now()
	c.tokens = min(h.cfg.MsgBurst, c.tokens+now.Sub(c.tokensAt).Seconds()*h.cfg.MsgPerSecond)
	c.tokensAt = now
	if c.tokens >= cost {
		c.tokens -= cost
		return true
	}
	if now.Sub(c.violationAt) > time.Minute {
		c.violations = 0
	}
	c.violations++
	c.violationAt = now
	if c.violations >= 8 {
		c.violations = 0
		c.mutedUntil = now.Add(2 * time.Minute)
		h.errorf(c, "刷屏保护：禁言 2 分钟。", "Flood protection: muted for 2 minutes.")
		return false
	}
	h.errorf(c, "发送太快了，慢一点。", "Slow down — you're sending too fast.")
	return false
}

func (h *Hub) repeatLocked(c *Client, text string) bool {
	if c.admin {
		return false
	}
	if strings.EqualFold(text, c.lastText) {
		c.repeat++
	} else {
		c.lastText, c.repeat = text, 0
	}
	if c.repeat >= 2 {
		h.errorf(c, "请勿重复发送同一条消息。", "Don't repeat the same message.")
		return true
	}
	return false
}

// --- helpers ------------------------------------------------------------------

func (h *Hub) roomSystemLocked(r *room, text, en string) {
	if r == nil {
		return
	}
	h.eventID++
	ev := &Event{ID: h.eventID, Time: time.Now(), Kind: KindSystem, Room: r.name, Text: text, En: en}
	for _, m := range r.members {
		m.send(ev)
	}
}

// infof / errorf send a server message in both languages (Chinese, English);
// the viewer's UI shows one.
func (h *Hub) infof(c *Client, zh, en string, a ...any) {
	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf(zh, a...), En: fmt.Sprintf(en, a...)})
}

func (h *Hub) errorf(c *Client, zh, en string, a ...any) {
	c.send(&Event{Kind: KindError, Text: fmt.Sprintf(zh, a...), En: fmt.Sprintf(en, a...)})
}

// L picks the client's language for text that isn't an Event (e.g. a
// disconnect reason).
func (c *Client) L(zh, en string) string {
	if c.lang == "en" {
		return en
	}
	return zh
}

// normLang maps a locale such as "zh_CN.UTF-8", "en_US" or "" to "zh"/"en".
func normLang(v, def string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "zh"):
		return "zh"
	case v == "", v == "c", v == "posix", strings.HasPrefix(v, "c."):
		if def == "en" {
			return "en"
		}
		return "zh"
	default:
		return "en"
	}
}

func (r *room) topicFor(lang string) string {
	if lang == "en" && r.topicEn != "" {
		return r.topicEn
	}
	return r.topic
}

func (h *Hub) langLocked(c *Client, arg string) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "zh", "cn", "中文", "chinese":
		c.lang = "zh"
	case "en", "english", "英文":
		c.lang = "en"
	default:
		h.infof(c, "界面语言：中文。切换：/lang en", "Interface language: English. Switch: /lang zh")
		return
	}
	h.infof(c, "界面语言已切换为中文。", "Interface language set to English.")
}

// LangFor resolves a client locale ("zh_CN.UTF-8", "en", "") to "zh"/"en"
// using the server default.
func (h *Hub) LangFor(locale string) string { return normLang(locale, h.cfg.Lang) }

// Lang returns the client's interface language ("zh" or "en").
func (h *Hub) Lang(c *Client) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.lang
}

func (h *Hub) pruneHistory(hist []Event, now time.Time) []Event {
	cut := 0
	for cut < len(hist) && now.Sub(hist[cut].Time) > h.cfg.HistoryTTL {
		cut++
	}
	if cut == 0 {
		return hist
	}
	return append(hist[:0:0], hist[cut:]...)
}

func pruneTimes(ts []time.Time, after time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(after) {
		i++
	}
	return ts[i:]
}

func (h *Hub) freeNickLocked() string {
	for {
		n := fmt.Sprintf("anonymous_%04d", mrand.IntN(10000))
		if h.byNick[n] == nil {
			return n
		}
	}
}

const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func randomCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// --- status for front-ends -------------------------------------------------

// RoomInfo is one row of the room sidebar.
type RoomInfo struct {
	Name      string
	Label     string
	Users     int
	Current   bool
	Private   bool
	Unread    int  // messages since the client last looked at this room
	Mentioned bool // someone @mentioned the client there meanwhile
}

// Status is a snapshot for rendering headers and sidebars.
type Status struct {
	Nick      string
	Room      string
	RoomLabel string
	Topic     string
	Online    int
	RoomUsers int
	Rooms     []RoomInfo
	Uptime    time.Duration
	Admin     bool
	Muted     bool
	Bell      bool   // ring on @mentions and private messages
	Lang      string // "zh" or "en"
	Loc       *time.Location
}

// Status returns the current view of the world for client c.
func (h *Hub) Status(c *Client) Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Status{
		Nick:   c.nick,
		Room:   c.room,
		Online: len(h.clients),
		Uptime: time.Since(h.started),
		Admin:  c.admin,
		Muted:  !c.mutedUntil.IsZero() && time.Now().Before(c.mutedUntil),
		Loc:    c.loc,
		Bell:   !c.noBell,
		Lang:   c.lang,
	}
	s.RoomLabel = h.roomLabel(c.room)
	if r := h.rooms[c.room]; r != nil {
		s.RoomUsers = len(r.members)
		s.Topic = r.topicFor(c.lang)
		if r.private {
			s.Topic = c.L("私人房间", "private room")
		}
	}
	for _, name := range h.public {
		r := h.rooms[name]
		ri := RoomInfo{Name: name, Label: "#" + name, Users: len(r.members), Current: name == c.room}
		if !ri.Current {
			ri.Unread = int(r.seq - c.seen[name])
			ri.Mentioned = c.mentioned[name]
		}
		s.Rooms = append(s.Rooms, ri)
	}
	if r := h.rooms[c.room]; r != nil && r.private {
		s.Rooms = append(s.Rooms, RoomInfo{Name: r.name, Label: h.roomLabel(r.name), Users: len(r.members), Current: true, Private: true})
	}
	return s
}

// Nick returns the client's current nickname.
func (h *Hub) Nick(c *Client) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.nick
}

// Loc returns the client's display timezone.
func (h *Hub) Loc(c *Client) *time.Location {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.loc
}

// Nicks returns online nicknames, for tab completion.
func (h *Hub) Nicks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.clients))
	for _, c := range h.clients {
		out = append(out, c.nick)
	}
	return out
}

// Stats is a privacy-safe summary for health checks.
type Stats struct {
	Online       int   `json:"online"`
	Rooms        int   `json:"rooms"`
	PrivateRooms int   `json:"private_rooms"`
	UptimeSec    int64 `json:"uptime_seconds"`
}

// Stats returns counts only — no nicknames, addresses or messages.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Stats{Online: len(h.clients), UptimeSec: int64(time.Since(h.started).Seconds())}
	for _, r := range h.rooms {
		if r.private {
			st.PrivateRooms++
		} else {
			st.Rooms++
		}
	}
	return st
}

func banID(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// Bans returns a copy of the active bans and a version number that changes
// whenever they do, so a persister can save only when needed.
func (h *Hub) Bans() (map[string]time.Time, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]time.Time, len(h.bans))
	now := time.Now()
	for k, v := range h.bans {
		if v.After(now) {
			out[k] = v
		}
	}
	return out, h.banVer
}

// LoadBans installs previously saved bans (expired ones are skipped).
func (h *Hub) LoadBans(b map[string]time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	now := time.Now()
	for k, v := range b {
		if v.After(now) && k != OnionKey {
			h.bans[k] = v
			n++
		}
	}
	return n
}
