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
	ID      uint64 // non-zero for events broadcast to a room (same for every viewer)
	Time    time.Time
	Kind    Kind
	Room    string
	From    string
	To      string
	Text    string
	Lines   []string // extra lines for multi-line info blocks
	History bool     // replayed from the room buffer on join
	sender  uint64   // client ID of the author (never shown), for /forget
}

// RoomDef describes a public room created at startup.
type RoomDef struct {
	Name  string
	Topic string
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
}

func (c *Config) defaults() {
	if len(c.PublicRooms) == 0 {
		c.PublicRooms = []RoomDef{
			{"general", "大厅 · Lobby — 什么都可以聊"},
			{"chinese", "中文频道"},
			{"english", "English channel"},
			{"technology", "编程、Linux、网络、安全 · Tech talk"},
			{"anonymous", "随便聊 · Off-topic"},
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
	private bool
	members map[uint64]*Client
	history []Event
	seq     uint64 // chat/action messages ever posted, for unread counts
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
		h.rooms[r.Name] = &room{name: r.Name, topic: r.Topic, members: map[uint64]*Client{}}
		h.public = append(h.public, r.Name)
	}
	return h
}

// Config returns the effective configuration.
func (h *Hub) Config() Config { return h.cfg }

// Connect registers a new anonymous session coming from ipKey (an opaque,
// already-hashed address key). The client starts in the first public room.
func (h *Hub) Connect(ipKey string, admin bool) (*Client, error) {
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

	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("Welcome, %s · 欢迎！你的身份是随机分配的。", c.nick), Lines: []string{
		"输入文字后按 Enter 发送。/help 查看全部命令。",
		"Type and press Enter to talk. /help lists all commands.",
		"提醒：SSH 只加密传输，服务器能看到消息内容；不要发送真实身份信息。",
		"本服务器存什么、不存什么：/privacy    删除你发过的消息：/forget",
	}})
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
		h.roomSystemLocked(r, fmt.Sprintf("%s %s.", c.nick, verb))
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
		c.send(&Event{Kind: KindNotice, Text: msg})
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
				h.removeLocked(c, fmt.Sprintf("idle for %s · 长时间未发言，已断开", h.cfg.IdleKick), "timed out")
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
		h.helpLocked(c)
	case "rooms", "list":
		h.roomsLocked(c)
	case "join", "j":
		h.cmdJoinLocked(c, rest)
	case "create", "new":
		h.cmdCreateLocked(c)
	case "leave", "part", "lobby":
		if c.room == h.public[0] {
			h.errorf(c, "You are already in #%s.", c.room)
		} else {
			h.joinLocked(c, h.public[0])
		}
	case "msg", "m", "w", "tell", "dm":
		target, text, _ := strings.Cut(rest, " ")
		h.dmLocked(c, strings.TrimPrefix(target, "@"), text)
	case "r", "reply":
		if c.lastDMFrom == "" {
			h.errorf(c, "Nobody has messaged you yet. 还没有人私信你。")
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
	case "privacy", "policy":
		h.privacyLocked(c)
	case "forget":
		h.forgetLocked(c)
	case "tz", "time":
		h.tzLocked(c, rest)
	case "whoami":
		h.infof(c, "You are %s in %s.", c.nick, h.roomLabel(c.room))
	// --- admin ---
	case "kick", "mute", "unmute", "ban", "unbanall", "bans", "unban", "broadcast", "announce", "topic", "reports":
		if !c.admin {
			h.errorf(c, "Unknown command /%s — try /help", cmd)
			return Result{}
		}
		h.adminLocked(c, cmd, rest)
	default:
		h.errorf(c, "Unknown command /%s — try /help · 未知命令", cmd)
	}
	return Result{}
}

func (h *Hub) helpLocked(c *Client) {
	lines := []string{
		"/rooms                 列出公共频道 · list public rooms",
		"/join <room|code>      进入频道或私人房间 · join a room or private code",
		"/create                创建私人房间（凭码进入）· new private room",
		"/leave                 回到 #general · back to the lobby",
		"/msg <nick> <text>     私聊 · private message   (/r <text> 回复)",
		"/nick <name>           修改昵称 · change nickname",
		"/who                   当前房间在线用户 · who is here",
		"/me <action>           动作消息 · action",
		"/ignore <nick>         屏蔽某人（/unignore 取消）· hide someone",
		"/report <nick> <why>   举报给管理员 · report to moderators",
		"/tz <zone>             时区，如 /tz Asia/Shanghai 或 /tz +8 · timezone",
		"/privacy               本服务器的数据政策 · what this server keeps",
		"/forget                从房间历史删除你的消息 · erase your messages",
		"房间栏 +N = 其他房间的未读消息，@N = 有人在那边 @了你 · +N unread, @N mentioned",
		"/clear                 清屏 · clear screen",
		"/quit                  退出 · disconnect",
		"//text                 发送以 / 开头的文字 · send a literal '/'",
	}
	if c.admin {
		lines = append(lines,
			"── admin ──",
			"/kick <nick> [reason]  /mute <nick> [min]  /unmute <nick>",
			"/ban <nick> [min]      /unbanall  /broadcast <text>  /topic <text>",
			"/reports               最近的举报 · recent reports",
			"/bans                  封禁列表 · list bans    /unban <id>  解封",
		)
	}
	c.send(&Event{Kind: KindInfo, Text: "Commands · 命令", Lines: lines})
}

func (h *Hub) roomsLocked(c *Client) {
	var lines []string
	for _, name := range h.public {
		r := h.rooms[name]
		mark := "  "
		if c.room == name {
			mark = "▸ "
		}
		extra := ""
		if n := r.seq - c.seen[name]; c.room != name && n > 0 {
			extra = fmt.Sprintf("  (%d new", n)
			if c.mentioned[name] {
				extra += ", @you"
			}
			extra += ")"
		}
		lines = append(lines, fmt.Sprintf("%s#%-12s %3d users   %s%s", mark, name, len(r.members), r.topic, extra))
	}
	if r := h.rooms[c.room]; r != nil && r.private {
		lines = append(lines, fmt.Sprintf("▸ %s (private) %d users", h.roomLabel(c.room), len(r.members)))
	}
	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("Rooms · 频道 — %d online", len(h.clients)), Lines: lines})
}

func (h *Hub) cmdJoinLocked(c *Client, name string) {
	name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "#~"))
	if name == "" {
		h.errorf(c, "Usage: /join <room>   e.g. /join chinese")
		return
	}
	r := h.rooms[name]
	if r == nil {
		h.errorf(c, "No such room %q. /rooms lists public rooms; /create makes a private one.", name)
		return
	}
	if c.room == name {
		h.errorf(c, "You are already in %s.", h.roomLabel(name))
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
		h.errorf(c, "Too many private rooms right now, try later.")
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
	h.infof(c, "Private room created. 把这个房间码发给朋友：/join %s", code)
	h.infof(c, "The room disappears (with its history) when the last person leaves.")
}

func (h *Hub) joinLocked(c *Client, name string) {
	if old := h.rooms[c.room]; old != nil {
		c.seen[old.name] = old.seq
		delete(old.members, c.ID)
		h.roomSystemLocked(old, fmt.Sprintf("%s left for another room.", c.nick))
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
	r.members[c.ID] = c
	h.roomSystemLocked(r, fmt.Sprintf("%s joined.", c.nick))
	topic := r.topic
	if r.private {
		topic = "private room · 私人房间"
	}
	c.send(&Event{Kind: KindInfo, Room: name, Text: fmt.Sprintf("Now in %s — %s — %d here", h.roomLabel(name), topic, len(r.members))})
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
			h.errorf(c, "Usage: /me <action>")
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
		h.errorf(c, "Usage: /msg <nick> <message>")
		return
	}
	to := h.byNick[strings.ToLower(target)]
	if to == nil {
		h.errorf(c, "No user named %q online. 用户不在线。", target)
		return
	}
	if to == c {
		h.errorf(c, "Talking to yourself? 😄")
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
		h.infof(c, "You are %s. Usage: /nick <new name>", c.nick)
		return
	}
	if !ValidNick(n) {
		h.errorf(c, "Nickname must be 2–16 letters/digits/_/- (中文可以). 昵称不合法。")
		return
	}
	if reservedNick(n) && !c.admin {
		h.errorf(c, "%q is reserved. 该昵称保留。", n)
		return
	}
	if n == c.nick {
		return
	}
	if o := h.byNick[strings.ToLower(n)]; o != nil && o != c {
		h.errorf(c, "%q is taken. 昵称已被占用。", n)
		return
	}
	if h.mutedLocked(c) {
		return
	}
	if wait := c.nickAt.Add(h.cfg.NickCooldown).Sub(time.Now()); wait > 0 && !c.admin {
		h.errorf(c, "Wait %ds before changing nick again.", int(wait.Seconds())+1)
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
	h.roomSystemLocked(h.rooms[c.room], fmt.Sprintf("%s is now known as %s.", old, n))
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
		lines = append(lines, fmt.Sprintf("  … and %d more", more))
	}
	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("%d in %s · %d online total", len(r.members), h.roomLabel(c.room), len(h.clients)), Lines: lines})
}

func (h *Hub) adminLocked(c *Client, cmd, rest string) {
	arg, tail, _ := strings.Cut(rest, " ")
	tail = strings.TrimSpace(tail)
	switch cmd {
	case "broadcast", "announce":
		text := Sanitize(rest, h.cfg.MaxMsgRunes)
		if text == "" {
			h.errorf(c, "Usage: /broadcast <text>")
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
		h.infof(c, "Cleared %d bans.", n)
		return
	case "bans":
		if len(h.bans) == 0 {
			h.infof(c, "No active bans. 当前没有封禁。")
			return
		}
		keys := make([]string, 0, len(h.bans))
		for k := range h.bans {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return h.bans[keys[i]].Before(h.bans[keys[j]]) })
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("  %s   %s left", banID(k), time.Until(h.bans[k]).Round(time.Minute)))
		}
		c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("%d active bans (id = anonymous address hash) · /unban <id>", len(keys)), Lines: lines})
		return
	case "unban":
		id := strings.ToLower(strings.TrimSpace(rest))
		for k := range h.bans {
			if id != "" && banID(k) == id {
				delete(h.bans, k)
				h.banVer++
				h.infof(c, "Unbanned %s.", id)
				return
			}
		}
		h.errorf(c, "No ban with id %q — see /bans", id)
		return
	case "reports":
		if len(h.reports) == 0 {
			h.infof(c, "No reports. 暂无举报。")
			return
		}
		c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf("%d recent reports", len(h.reports)), Lines: append([]string(nil), h.reports...)})
		return
	case "topic":
		r := h.rooms[c.room]
		r.topic = Sanitize(rest, 80)
		h.roomSystemLocked(r, fmt.Sprintf("Topic set to: %s", r.topic))
		return
	}
	t := h.byNick[strings.ToLower(strings.TrimPrefix(arg, "@"))]
	if t == nil {
		h.errorf(c, "Usage: /%s <nick> … (user not found)", cmd)
		return
	}
	if t.admin && t != c {
		h.errorf(c, "Cannot %s another admin.", cmd)
		return
	}
	minutes := 0
	fmt.Sscanf(tail, "%d", &minutes)
	switch cmd {
	case "kick":
		reason := "kicked by admin"
		if tail != "" {
			reason = "kicked: " + Sanitize(tail, 120)
		}
		h.removeLocked(t, reason, "was kicked")
	case "mute":
		if minutes <= 0 {
			minutes = 10
		}
		t.mutedUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		t.send(&Event{Kind: KindError, Text: fmt.Sprintf("You were muted for %d min by an admin. 你被禁言 %d 分钟。", minutes, minutes)})
		h.infof(c, "Muted %s for %d min.", t.nick, minutes)
	case "unmute":
		t.mutedUntil = time.Time{}
		t.violations = 0
		t.send(&Event{Kind: KindInfo, Text: "You can talk again. 你已解除禁言。"})
		h.infof(c, "Unmuted %s.", t.nick)
	case "ban":
		if t.ipKey == OnionKey { // banning the shared Tor key would lock out every Tor user
			h.removeLocked(t, "kicked by admin", "was kicked")
			h.infof(c, "%s came via Tor and can't be banned by address — kicked instead. Use /mute for repeat offenders.", arg)
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
				h.removeLocked(o, fmt.Sprintf("banned for %d min", minutes), "was banned")
			}
		}
		h.infof(c, "Banned %s's address for %d min.", arg, minutes)
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
			h.infof(c, "You aren't ignoring anyone. Usage: /ignore <nick>")
		} else {
			sort.Strings(names)
			h.infof(c, "Ignoring: %s", strings.Join(names, ", "))
		}
		return
	}
	t := h.byNick[strings.ToLower(nick)]
	if t == nil {
		h.errorf(c, "No user named %q online.", nick)
		return
	}
	if t == c {
		h.errorf(c, "You can't ignore yourself.")
		return
	}
	if on {
		c.ignores[t.ID] = true
		h.infof(c, "Ignoring %s — you won't see their messages (until they reconnect). 已屏蔽。", t.nick)
	} else {
		delete(c.ignores, t.ID)
		h.infof(c, "No longer ignoring %s. 已取消屏蔽。", t.nick)
	}
}

func (h *Hub) reportLocked(c *Client, nick, reason string) {
	reason = Sanitize(reason, 200)
	t := h.byNick[strings.ToLower(nick)]
	if nick == "" || t == nil {
		h.errorf(c, "Usage: /report <nick> <reason> (user must be online)")
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
			o.send(&Event{Kind: KindNotice, Text: "Report · 举报", Lines: []string{line}})
		}
	}
	if admins > 0 {
		h.infof(c, "Report sent to %d moderator(s). 已通知管理员。", admins)
	} else {
		h.infof(c, "Report saved; no moderator is online right now. 已记录，管理员上线后可见。")
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
		h.infof(c, "Times are shown in %s (now %s). Usage: /tz Asia/Shanghai | /tz America/New_York | /tz +8 | /tz utc",
			c.loc.String(), time.Now().In(c.loc).Format("15:04"))
		return
	}
	loc, err := ParseZone(arg)
	if err != nil {
		h.errorf(c, "Unknown timezone %q. 例如 /tz Asia/Shanghai 或 /tz +8", Sanitize(arg, 40))
		return
	}
	c.loc = loc
	h.infof(c, "Timezone set to %s — now %s. 时区已设置。", loc.String(), time.Now().In(loc).Format("15:04"))
}

// privacyLocked tells the user, from the live configuration, what this
// server keeps about them and what it can't promise.
func (h *Hub) privacyLocked(c *Client) {
	bans := "仅在内存，重启即清空 · memory only"
	if h.cfg.PersistBans {
		bans = "已开启持久化：仅被封禁地址的哈希写入磁盘 · persisted (hashes of banned addresses only)"
	}
	tor := "未开启 · not enabled on this server"
	if h.cfg.TorEnabled {
		tor = "已开启，可通过 .onion 地址连接，服务器看不到你的 IP · available"
	}
	idle := "不限 · none"
	if h.cfg.IdleKick > 0 {
		idle = h.cfg.IdleKick.String()
	}
	lines := []string{
		fmt.Sprintf("消息   只在内存里，每个房间最多 %d 条、最长 %s，然后自动删除；私聊不保存", h.cfg.HistorySize, h.cfg.HistoryTTL),
		"       messages live in memory only and expire; private messages are never stored",
		"不记录 IP、昵称、消息内容都不写入日志；不收集 SSH 公钥；没有账号",
		"       no IPs, nicknames or message text in logs; no public keys; no accounts",
		"地址   只用来限流和封禁，以匿名哈希保存，不显示给任何人（包括管理员）",
		"封禁   " + bans,
		"Tor    " + tor,
		"空闲   " + idle + " 不发言自动断开",
		"管理员 可以踢出、禁言、封禁（都会公开显示）；看不到私聊，聊天室里也没有查看 IP 的功能",
		"删除   /forget 立刻从房间历史中删除你本次连接发过的所有消息",
		"边界   SSH 只加密传输：服务器进程能看到明文，VPS 运营者在系统层面能看到连接来源 IP。",
		"       想对运营者也隐藏 IP 请用 Tor；任何时候都不要发送能识别你身份的信息。",
	}
	c.send(&Event{Kind: KindInfo, Text: "本服务器的数据政策 · Privacy on this server", Lines: lines})
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
	h.infof(c, "已从房间历史删除你的 %d 条消息；已经显示在别人屏幕上的无法撤回。Erased %d of your messages from room history.", n, n)
}

// --- abuse protection -------------------------------------------------------

func (h *Hub) mutedLocked(c *Client) bool {
	if c.mutedUntil.IsZero() || time.Now().After(c.mutedUntil) {
		return false
	}
	left := time.Until(c.mutedUntil).Round(time.Second)
	h.errorf(c, "You are muted for %s more. 你正在禁言中。", left)
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
		h.errorf(c, "Flood protection: muted for 2 minutes. 刷屏保护：禁言 2 分钟。")
		return false
	}
	h.errorf(c, "Slow down — you're sending too fast. 发送太快了。")
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
		h.errorf(c, "Don't repeat the same message. 请勿重复发送。")
		return true
	}
	return false
}

// --- helpers ------------------------------------------------------------------

func (h *Hub) roomSystemLocked(r *room, text string) {
	if r == nil {
		return
	}
	h.eventID++
	ev := &Event{ID: h.eventID, Time: time.Now(), Kind: KindSystem, Room: r.name, Text: text}
	for _, m := range r.members {
		m.send(ev)
	}
}

func (h *Hub) infof(c *Client, format string, a ...any) {
	c.send(&Event{Kind: KindInfo, Text: fmt.Sprintf(format, a...)})
}

func (h *Hub) errorf(c *Client, format string, a ...any) {
	c.send(&Event{Kind: KindError, Text: fmt.Sprintf(format, a...)})
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
	}
	s.RoomLabel = h.roomLabel(c.room)
	if r := h.rooms[c.room]; r != nil {
		s.RoomUsers = len(r.members)
		s.Topic = r.topic
		if r.private {
			s.Topic = "private room · 私人房间"
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
