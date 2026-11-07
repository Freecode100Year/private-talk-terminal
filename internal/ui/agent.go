package ui

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Freecode100Year/vivant/internal/chat"
)

// Agent runs a bot-mode session (`ssh -T bot@host`): one JSON object per
// line in both directions, so an AI agent or script never has to parse the
// human-oriented, bilingual chat lines. The session is marked as a bot, and
// its messages are labelled "·bot" for everyone else.
//
// Output: see AgentEvent. Input, one per line, either
//
//	plain text            sent exactly as if typed (commands work: /join tech)
//	{"text":"..."}        a message for the current room; may be long or
//	                      contain newlines: it's split into chat-sized
//	                      messages and paced to the rate limit
//	{"to":"nick","text":"..."}  the same, as a private message
//	{"cmd":"/who"}        one command
//	{"type":"ping"}       answered with {"type":"pong"} (doesn't count as activity)
func Agent(rw io.ReadWriter, hub *chat.Hub, c *chat.Client) {
	hub.SetBot(c)
	a := &agent{w: rw, hub: hub, c: c, enc: json.NewEncoder(rw)}
	cfg := hub.Config()
	st := hub.Status(c)
	a.emit(AgentEvent{Type: "hello", Nick: st.Nick, Room: st.Room, Time: time.Now(), Limits: &agentLimits{
		MaxRunes:  cfg.MaxMsgRunes,
		Burst:     cfg.MsgBurst,
		PerSecond: cfg.MsgPerSecond,
		IdleKick:  int(cfg.IdleKick / time.Second),
		MaxSplit:  agentMaxSplit,
	}})

	in := make(chan agentInput)
	quit := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go a.readInput(rw, in, quit, stop)

	// Outgoing chunks wait here so a long reply doesn't block reading
	// events; the pacer keeps them within the server's rate limit.
	queue := make(chan string, 4*agentMaxSplit)
	go a.pace(queue, stop)

	for {
		select {
		case ev := <-c.Out:
			if ev.Kind == chat.KindHistory {
				for _, e := range ev.Batch {
					a.emitEvent(e)
				}
				continue
			}
			a.emitEvent(ev)
		case <-c.Done:
			a.emit(AgentEvent{Type: "disconnect", Text: c.Reason(), Time: time.Now()})
			return
		case <-quit:
			// Input ended (`echo ... | ssh -T bot@host`): send what's queued first.
			deadline := time.After(time.Duration(float64(len(queue)+1)/max(cfg.MsgPerSecond, 0.01)*float64(time.Second)) + 2*time.Second)
			for a.pending.Load() > 0 {
				select {
				case <-c.Done:
					return
				case <-deadline:
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			return
		case inp := <-in:
			switch {
			case inp.ping:
				a.emit(AgentEvent{Type: "pong", Time: time.Now()})
			case inp.cmd != "":
				if hub.Handle(c, inp.cmd).Quit {
					a.emit(AgentEvent{Type: "bye", Time: time.Now()})
					return
				}
			case inp.err != "":
				a.emit(AgentEvent{Type: "error", Text: "输入无效：" + inp.err, En: "Bad input: " + inp.err, Time: time.Now()})
			default:
				for _, m := range inp.msgs {
					a.pending.Add(1)
					select {
					case queue <- m:
					default:
						a.pending.Add(-1)
						a.emit(AgentEvent{Type: "error", Text: "发送队列已满，消息被丢弃", En: "Send queue full — message dropped", Time: time.Now()})
					}
				}
			}
		}
	}
}

// agentMaxSplit caps how many chat messages one input may turn into, so a
// runaway model can't flood the room.
const agentMaxSplit = 8

type agentLimits struct {
	MaxRunes  int     `json:"max_runes"`  // longer messages are split (bot mode) or cut
	Burst     float64 `json:"burst"`      // messages that may be sent at once
	PerSecond float64 `json:"per_second"` // sustained message rate
	IdleKick  int     `json:"idle_kick"`  // seconds without posting before disconnect (0 = never)
	MaxSplit  int     `json:"max_split"`  // most messages one {"text":...} input becomes
}

// AgentEvent is one line of bot-mode output.
type AgentEvent struct {
	// hello, chat, action, system, dm, info, error, notice, mention,
	// pong, disconnect, bye
	Type    string       `json:"type"`
	ID      uint64       `json:"id,omitempty"` // same for every viewer of a room message
	Time    time.Time    `json:"time"`
	Room    string       `json:"room,omitempty"`
	From    string       `json:"from,omitempty"`
	To      string       `json:"to,omitempty"`
	Nick    string       `json:"nick,omitempty"` // hello: your nick
	Text    string       `json:"text,omitempty"` // message text; for server messages, the Chinese
	En      string       `json:"en,omitempty"`   // server messages: the English
	Lines   []string     `json:"lines,omitempty"`
	LinesEn []string     `json:"lines_en,omitempty"`
	Self    bool         `json:"self,omitempty"`     // you sent it
	Mention bool         `json:"mention,omitempty"`  // it @mentions you, or is a private message to you
	Bot     bool         `json:"bot,omitempty"`      // the author is a bot
	History bool         `json:"history,omitempty"`  // replayed on join, not new
	In      bool         `json:"incoming,omitempty"` // dm: true = received, false = your own, echoed
	Limits  *agentLimits `json:"limits,omitempty"`
}

var agentKinds = map[chat.Kind]string{
	chat.KindChat: "chat", chat.KindAction: "action", chat.KindSystem: "system",
	chat.KindDMIn: "dm", chat.KindDMOut: "dm", chat.KindInfo: "info",
	chat.KindError: "error", chat.KindNotice: "notice", chat.KindMention: "mention",
}

type agent struct {
	mu  sync.Mutex
	w   io.Writer
	enc *json.Encoder
	hub *chat.Hub
	c   *chat.Client
	// pending counts queued messages not yet handed to the hub.
	pending atomic.Int32
}

func (a *agent) emit(e AgentEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.enc.Encode(e) // Encode appends "\n"
}

func (a *agent) emitEvent(ev *chat.Event) {
	self := a.hub.Nick(a.c)
	e := AgentEvent{
		Type: agentKinds[ev.Kind], ID: ev.ID, Time: ev.Time, Room: ev.Room, From: ev.From, To: ev.To,
		Text: ev.Text, En: ev.En, Lines: ev.Lines, LinesEn: ev.LinesEn, Bot: ev.Bot, History: ev.History,
	}
	if e.Type == "" {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	switch ev.Kind {
	case chat.KindChat, chat.KindAction:
		e.Self = ev.From == self
		e.Mention = !e.Self && Mentions(ev.Text, self)
	case chat.KindDMIn:
		e.In, e.Mention = true, true
	case chat.KindDMOut:
		e.Self = true
	case chat.KindMention:
		e.Mention = true
	}
	a.emit(e)
}

// pace sends queued messages no faster than the hub's token bucket refills,
// so split replies arrive instead of tripping flood protection.
func (a *agent) pace(queue <-chan string, stop <-chan struct{}) {
	cfg := a.hub.Config()
	rate := max(cfg.MsgPerSecond, 0.01)
	burst := max(cfg.MsgBurst-1, 1) // keep one token spare for commands
	tokens, at := burst, time.Now()
	for {
		select {
		case <-stop:
			return
		case m := <-queue:
			now := time.Now()
			tokens = min(burst, tokens+now.Sub(at).Seconds()*rate)
			at = now
			if tokens < 1 {
				wait := time.Duration((1 - tokens) / rate * float64(time.Second))
				select {
				case <-time.After(wait + 20*time.Millisecond):
				case <-stop:
					return
				}
				tokens, at = 1, time.Now()
			}
			tokens--
			a.hub.Handle(a.c, m)
			a.pending.Add(-1)
		}
	}
}

type agentInput struct {
	cmd  string   // a line to hand to Hub.Handle as typed
	msgs []string // chat-sized lines to send through the pacer
	ping bool
	err  string
}

type agentJSON struct {
	Type string `json:"type"`
	Text string `json:"text"`
	To   string `json:"to"`
	Cmd  string `json:"cmd"`
}

func (a *agent) parse(line string, maxRunes int) agentInput {
	line = strings.TrimSpace(line)
	if line == "" {
		return agentInput{}
	}
	if line[0] != '{' {
		return agentInput{cmd: line}
	}
	var j agentJSON
	if err := json.Unmarshal([]byte(line), &j); err != nil {
		return agentInput{err: err.Error()}
	}
	switch {
	case j.Type == "ping":
		return agentInput{ping: true}
	case j.Cmd != "":
		if !strings.HasPrefix(j.Cmd, "/") {
			return agentInput{err: `"cmd" must start with "/"`}
		}
		return agentInput{cmd: strings.ReplaceAll(j.Cmd, "\n", " ")}
	case strings.TrimSpace(j.Text) == "":
		return agentInput{err: `expected "text", "cmd" or "type":"ping"`}
	}
	prefix := ""
	if j.To != "" {
		if strings.ContainsAny(j.To, " \t\r\n") {
			return agentInput{err: `"to" must be one nick`}
		}
		prefix = "/msg " + j.To + " "
	}
	var msgs []string
	for _, part := range SplitMessage(j.Text, maxRunes, agentMaxSplit) {
		if prefix == "" && strings.HasPrefix(part, "/") {
			part = "/" + part // text is never a command: "//x" sends "/x"
		}
		msgs = append(msgs, prefix+part)
	}
	return agentInput{msgs: msgs}
}

// SplitMessage turns text into at most maxParts chat messages of at most
// maxRunes runes each: one per non-empty line, long lines broken at a space
// where possible. If it doesn't fit, the last part ends with "…".
func SplitMessage(text string, maxRunes, maxParts int) []string {
	if maxRunes < 2 {
		maxRunes = 2
	}
	var out []string
	full := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		for line != "" {
			if len(out) == maxParts {
				full = true
				break
			}
			if utf8.RuneCountInString(line) <= maxRunes {
				out = append(out, line)
				break
			}
			cut := runeOffset(line, maxRunes)
			if sp := strings.LastIndexByte(line[:cut], ' '); sp > cut/2 {
				cut = sp
			}
			out = append(out, strings.TrimSpace(line[:cut]))
			line = strings.TrimSpace(line[cut:])
		}
		if full {
			break
		}
	}
	if full && len(out) > 0 {
		last := []rune(out[len(out)-1])
		if len(last) >= maxRunes {
			last = last[:maxRunes-1]
		}
		out[len(out)-1] = string(last) + "…"
	}
	return out
}

// runeOffset is the byte offset of the n-th rune of s.
func runeOffset(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
}

func (a *agent) readInput(r io.Reader, in chan<- agentInput, quit chan<- struct{}, stop <-chan struct{}) {
	defer close(quit)
	maxRunes := a.hub.Config().MaxMsgRunes
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 256*1024)
	sc.Split(scanLinesCR) // with a PTY, Enter sends "\r"
	for sc.Scan() {
		inp := a.parse(sc.Text(), maxRunes)
		if inp.cmd == "" && inp.msgs == nil && !inp.ping && inp.err == "" {
			continue
		}
		select {
		case in <- inp:
		case <-stop:
			return
		}
	}
}

// scanLinesCR splits on "\n", "\r" or "\r\n".
func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			adv := i + 1
			if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
				adv++
			} else if b == '\r' && i+1 == len(data) && !atEOF {
				return 0, nil, nil // might be "\r\n" split across reads
			}
			return adv, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
