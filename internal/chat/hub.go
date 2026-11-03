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
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind classifies an Event so front-ends can style it.
type Kind int

const (
	KindChat   Kind = iota // <nick> text
	KindAction             // * nick does something
	KindSystem             // joins, leaves, nick changes
	KindDMIn               // private message received
	KindDMOut              // private message sent (echo to sender)
	KindInfo               // command output for this user only
	KindError              // command error for this user only
	KindNotice             // admin broadcast
)

// Event is one line (or block) of output for a client.
type Event struct {
	Time    time.Time
	Kind    Kind
	Room    string
	From    string
	To      string
	Text    string
	Lines   []string // extra lines for multi-line info blocks
	History bool     // replayed from the room buffer on join
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
}

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
}

// Client is one connected session.
type Client struct {
	ID    uint64
	Out   <-chan Event
	Done  <-chan struct{} // closed when the hub drops this client (kick/ban/disconnect)
	out   chan Event
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
func (c *Client) send(ev Event) {
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
		delete(h.bans, ipKey)
	}
	if !admin {
		if len(h.clients) >= h.cfg.MaxClients {
			return nil, ErrFull
		}
		if h.perIP[ipKey] >= h.cfg.MaxConnsPerIP {
			return nil, ErrTooManyConns
		}
		log := pruneTimes(h.connLog[ipKey], now.Add(-time.Minute))
		if len(log) >= h.cfg.ConnPerMinute {
			h.connLog[ipKey] = log
			return nil, ErrConnRate
		}
		h.connLog[ipKey] = append(log, now)
	}

	h.nextID++
	c := &Client{
		ID:        h.nextID,
		out:       make(chan Event, 512),
		done:      make(chan struct{}),
		ipKey:     ipKey,
		admin:     admin,
		tokens:    h.cfg.MsgBurst,
		tokensAt:  now,
		createdAt: now,
	}
	c.Out, c.Done = c.out, c.done
	c.nick = h.freeNickLocked()
	h.clients[c.ID] = c
	h.byNick[strings.ToLower(c.nick)] = c
	h.perIP[ipKey]++

	c.send(Event{Kind: KindInfo, Text: fmt.Sprintf("Welcome, %s · 欢迎！你的身份是随机分配的。", c.nick), Lines: []string{
		"输入文字后按 Enter 发送。/help 查看全部命令。",
		"Type and press Enter to talk. /help lists all commands.",
		"提醒：SSH 只加密传输，服务器能看到消息内容；不要发送真实身份信息。",
	}})
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
		c.send(Event{Kind: KindNotice, Text: msg})
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
	case "whoami":
		h.infof(c, "You are %s in %s.", c.nick, h.roomLabel(c.room))
	// --- admin ---
	case "kick", "mute", "unmute", "ban", "unbanall", "broadcast", "announce", "topic":
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
		"/clear                 清屏 · clear screen",
		"/quit                  退出 · disconnect",
		"//text                 发送以 / 开头的文字 · send a literal '/'",
	}
	if c.admin {
		lines = append(lines,
			"── admin ──",
			"/kick <nick> [reason]  /mute <nick> [min]  /unmute <nick>",
			"/ban <nick> [min]      /unbanall  /broadcast <text>  /topic <text>",
		)
	}
	c.send(Event{Kind: KindInfo, Text: "Commands · 命令", Lines: lines})
}

func (h *Hub) roomsLocked(c *Client) {
	var lines []string
	for _, name := range h.public {
		r := h.rooms[name]
		mark := "  "
		if c.room == name {
			mark = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s#%-12s %3d users   %s", mark, name, len(r.members), r.topic))
	}
	if r := h.rooms[c.room]; r != nil && r.private {
		lines = append(lines, fmt.Sprintf("▸ %s (private) %d users", h.roomLabel(c.room), len(r.members)))
	}
	c.send(Event{Kind: KindInfo, Text: fmt.Sprintf("Rooms · 频道 — %d online", len(h.clients)), Lines: lines})
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
		delete(old.members, c.ID)
		h.roomSystemLocked(old, fmt.Sprintf("%s left for another room.", c.nick))
		h.gcRoomLocked(old)
	}
	r := h.rooms[name]
	c.room = name
	r.history = h.pruneHistory(r.history, time.Now())
	for _, ev := range r.history {
		ev.History = true
		c.send(ev)
	}
	r.members[c.ID] = c
	h.roomSystemLocked(r, fmt.Sprintf("%s joined.", c.nick))
	topic := r.topic
	if r.private {
		topic = "private room · 私人房间"
	}
	c.send(Event{Kind: KindInfo, Room: name, Text: fmt.Sprintf("Now in %s — %s — %d here", h.roomLabel(name), topic, len(r.members))})
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
	ev := Event{Time: time.Now(), Kind: kind, Room: r.name, From: c.nick, Text: text}
	r.history = append(r.history, ev)
	if len(r.history) > h.cfg.HistorySize {
		r.history = append(r.history[:0:0], r.history[len(r.history)-h.cfg.HistorySize:]...)
	}
	for _, m := range r.members {
		m.send(ev)
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
	to.lastDMFrom = c.nick
	to.send(Event{Time: now, Kind: KindDMIn, From: c.nick, To: to.nick, Text: text})
	c.send(Event{Time: now, Kind: KindDMOut, From: c.nick, To: to.nick, Text: text})
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
	c.send(Event{Kind: KindInfo, Text: fmt.Sprintf("%d in %s · %d online total", len(r.members), h.roomLabel(c.room), len(h.clients)), Lines: lines})
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
		for _, o := range h.clients {
			o.send(Event{Kind: KindNotice, From: c.nick, Text: text})
		}
		return
	case "unbanall":
		n := len(h.bans)
		h.bans = map[string]time.Time{}
		h.infof(c, "Cleared %d bans.", n)
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
		t.send(Event{Kind: KindError, Text: fmt.Sprintf("You were muted for %d min by an admin. 你被禁言 %d 分钟。", minutes, minutes)})
		h.infof(c, "Muted %s for %d min.", t.nick, minutes)
	case "unmute":
		t.mutedUntil = time.Time{}
		t.violations = 0
		t.send(Event{Kind: KindInfo, Text: "You can talk again. 你已解除禁言。"})
		h.infof(c, "Unmuted %s.", t.nick)
	case "ban":
		if minutes <= 0 {
			minutes = 60
		}
		h.bans[t.ipKey] = time.Now().Add(time.Duration(minutes) * time.Minute)
		key := t.ipKey
		for _, o := range h.clients {
			if o.ipKey == key && !o.admin {
				h.removeLocked(o, fmt.Sprintf("banned for %d min", minutes), "was banned")
			}
		}
		h.infof(c, "Banned %s's address for %d min.", arg, minutes)
	}
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
	ev := Event{Time: time.Now(), Kind: KindSystem, Room: r.name, Text: text}
	for _, m := range r.members {
		m.send(ev)
	}
}

func (h *Hub) infof(c *Client, format string, a ...any) {
	c.send(Event{Kind: KindInfo, Text: fmt.Sprintf(format, a...)})
}

func (h *Hub) errorf(c *Client, format string, a ...any) {
	c.send(Event{Kind: KindError, Text: fmt.Sprintf(format, a...)})
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
	Name    string
	Label   string
	Users   int
	Current bool
	Private bool
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
		s.Rooms = append(s.Rooms, RoomInfo{Name: name, Label: "#" + name, Users: len(h.rooms[name].members), Current: name == c.room})
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
