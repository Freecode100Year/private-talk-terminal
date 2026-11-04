// Package ui renders chat events for SSH sessions: a lightweight full-screen
// terminal UI and a plain line-mode fallback for old or minimal terminals.
package ui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
)

func nickColor(n string) sgr {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(n)))
	return nickPalette[h.Sum32()%uint32(len(nickPalette))]
}

// Formatter turns events into display lines.
type Formatter struct {
	Styled bool
	Self   func() string         // current nickname of the viewer
	Loc    func() *time.Location // viewer's timezone (nil = server local)
}

// bi joins a Chinese and an English text: every piece of interface text is
// shown in both languages.
func bi(zh, en string) string { return zh + " · " + en }

// joinWidth: a server message whose two halves fit in this many columns is
// shown on one line ("中文 · English"); longer ones put the English on the
// next line.
const joinWidth = 78

func (f Formatter) st(c sgr, s string) string {
	if !f.Styled {
		return s
	}
	return c.R(s)
}

// Rendered lines of room events are shared between sessions: everyone who
// sees a message the same way (same timezone, not their own message, not
// mentioning them) gets the same strings. A message to a busy room is then
// formatted a handful of times instead of once per connected user.
type renderKey struct {
	id   uint64
	loc  *time.Location
	hist bool
}

var (
	renderCache sync.Map // renderKey -> []string
	renderRing  = struct {
		sync.Mutex
		keys []renderKey
		pos  int
	}{keys: make([]renderKey, 8192)}
)

func cachePut(k renderKey, lines []string) {
	if _, loaded := renderCache.LoadOrStore(k, lines); loaded {
		return
	}
	renderRing.Lock()
	if old := renderRing.keys[renderRing.pos]; old.id != 0 {
		renderCache.Delete(old)
	}
	renderRing.keys[renderRing.pos] = k
	renderRing.pos = (renderRing.pos + 1) % len(renderRing.keys)
	renderRing.Unlock()
}

// Lines renders one event to one or more unwrapped lines. The returned slice
// may be shared: callers must not modify it.
func (f Formatter) Lines(ev chat.Event) []string {
	if !f.Styled || ev.ID == 0 {
		return f.lines(ev)
	}
	self := ""
	if f.Self != nil {
		self = f.Self()
	}
	if self != "" && (ev.From == self || Mentions(ev.Text, self)) {
		return f.lines(ev) // personalised rendering
	}
	var loc *time.Location
	if f.Loc != nil {
		loc = f.Loc()
	}
	k := renderKey{id: ev.ID, loc: loc, hist: ev.History}
	if v, ok := renderCache.Load(k); ok {
		return v.([]string)
	}
	lines := f.lines(ev)
	cachePut(k, lines)
	return lines
}

func (f Formatter) lines(ev chat.Event) []string {
	if ev.History && f.Styled { // replayed history: whole line dimmed
		lines := Formatter{Self: f.Self, Loc: f.Loc}.lines(ev)
		for i := range lines {
			lines[i] = sDim.R(lines[i])
		}
		return lines
	}
	// Server messages carry both languages: join them, or move the English
	// to its own line when the pair is long. ev is a copy.
	var enLine string
	if ev.En != "" && ev.En != ev.Text {
		if ev.Kind == chat.KindSystem || width(ev.Text)+3+width(ev.En) <= joinWidth {
			ev.Text = bi(ev.Text, ev.En)
		} else {
			enLine = ev.En
		}
	}
	t := ev.Time
	if t.IsZero() {
		t = time.Now()
	}
	if f.Loc != nil {
		if loc := f.Loc(); loc != nil {
			t = t.In(loc)
		}
	}
	ts := f.st(sDim, "["+t.Format("15:04")+"] ")
	self := ""
	if f.Self != nil {
		self = f.Self()
	}
	nick := func(n string) string {
		if !f.Styled {
			return n
		}
		if n == self {
			return sSelf.R(n)
		}
		return nickColor(n).R(n)
	}

	text := ev.Text
	if (ev.Kind == chat.KindChat || ev.Kind == chat.KindAction) && ev.From != self && Mentions(text, self) {
		if f.Styled {
			text = sMention.R(text)
		} else {
			text = "» " + text
		}
	}
	var head string
	switch ev.Kind {
	case chat.KindChat:
		head = ts + "<" + nick(ev.From) + "> " + text
	case chat.KindAction:
		head = ts + "* " + nick(ev.From) + " " + text
	case chat.KindSystem:
		head = ts + f.st(sDim, "-- "+ev.Text)
	case chat.KindDMIn:
		head = ts + f.st(sDM, "[私信 DM ← "+ev.From+"] ") + ev.Text
	case chat.KindDMOut:
		head = ts + f.st(sDM, "[私信 DM → "+ev.To+"] ") + ev.Text
	case chat.KindInfo:
		head = ts + f.st(sInfo, "* "+ev.Text)
	case chat.KindError:
		head = ts + f.st(sError, "! "+ev.Text)
	case chat.KindMention:
		head = ts + f.st(sMention, "@ #"+ev.Room+" <"+ev.From+"> ") + ev.Text + f.st(sDim, "  · /join "+ev.Room)
	case chat.KindNotice:
		from := "系统 System"
		if ev.From != "" {
			from = "管理员 Admin " + ev.From
		}
		head = ts + f.st(sNotice, fmt.Sprintf("📢 [%s] %s", from, ev.Text))
	default:
		head = ts + ev.Text
	}
	out := []string{head}
	body := func(l string, en bool) {
		switch {
		case en:
			out = append(out, "        "+f.st(sDim, l))
		case ev.Kind == chat.KindInfo || ev.Kind == chat.KindError:
			out = append(out, "        "+f.st(sInfo, l))
		default:
			out = append(out, "        "+l)
		}
	}
	if enLine != "" {
		body(enLine, true)
	}
	for i, l := range ev.Lines {
		body(l, false)
		if i < len(ev.LinesEn) && ev.LinesEn[i] != l {
			body(ev.LinesEn[i], true)
		}
	}
	return out
}

// Uptime formats a duration as "4d 12h" / "3h 5m" / "42m".
func Uptime(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// Mentions reports whether text mentions nick (case-insensitive, whole word
// for Latin nicknames; substring for CJK, which has no word boundaries).
func Mentions(text, nick string) bool {
	if nick == "" {
		return false
	}
	lt, ln := strings.ToLower(text), strings.ToLower(nick)
	for i := 0; ; {
		j := strings.Index(lt[i:], ln)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(ln)
		if !isWordByte(lt, start-1) && !isWordByte(lt, end) {
			return true
		}
		i = start + 1
	}
}

func isWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z'
}

// Rings reports whether an event should sound the terminal bell for the
// viewer self: a private message, a mention from another room, or a line
// here that mentions them. Replayed history never rings.
func Rings(ev *chat.Event, self string) bool {
	if ev.History {
		return false
	}
	switch ev.Kind {
	case chat.KindDMIn, chat.KindMention:
		return true
	case chat.KindChat, chat.KindAction:
		return ev.From != self && Mentions(ev.Text, self)
	}
	return false
}
