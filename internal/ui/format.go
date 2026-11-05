// Package ui renders chat events for SSH sessions: a lightweight full-screen
// terminal UI and a plain line-mode fallback for old or minimal terminals.
package ui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Freecode100Year/vivant/internal/chat"
)

func (t *Theme) nickColor(n string) paint {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(n)))
	return t.Nicks[h.Sum32()%uint32(len(t.Nicks))]
}

// Formatter turns events into display lines.
type Formatter struct {
	Styled bool
	Self   func() string         // current nickname of the viewer
	Loc    func() *time.Location // viewer's timezone (nil = server local)
	Theme  *Theme                // colours (nil = dark)
}

func (f Formatter) th() *Theme {
	if f.Theme == nil {
		return themeDark
	}
	return f.Theme
}

// bi joins a Chinese and an English text: every piece of interface text is
// shown in both languages.
func bi(zh, en string) string { return zh + " · " + en }

// joinWidth: a server message whose two halves fit in this many columns is
// shown on one line ("中文 · English"); longer ones put the English on the
// next line.
const joinWidth = 78

func (f Formatter) st(c paint, s string) string {
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
	id    uint64
	loc   *time.Location
	theme *Theme
	hist  bool
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
	k := renderKey{id: ev.ID, loc: loc, theme: f.th(), hist: ev.History}
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
			lines[i] = f.th().Dim.R(lines[i])
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
	th := f.th()
	ts := f.st(th.Dim, "["+t.Format("15:04")+"] ")
	self := ""
	if f.Self != nil {
		self = f.Self()
	}
	nick := func(n string) string {
		if !f.Styled {
			return n
		}
		if n == self {
			return th.Self.R(n)
		}
		return th.nickColor(n).R(n)
	}

	text := ev.Text
	if ev.Kind == chat.KindChat || ev.Kind == chat.KindAction {
		switch {
		case ev.From != self && Mentions(text, self):
			// a solid band plus a marker: visible without telling colours apart
			if f.Styled {
				text = th.Mention.R("» " + text + " ")
			} else {
				text = "» " + text
			}
		case f.Styled:
			text = highlightTags(text, th.Tag)
		}
	}
	var head string
	switch ev.Kind {
	case chat.KindChat:
		head = ts + "<" + nick(ev.From) + "> " + text
	case chat.KindAction:
		head = ts + "* " + nick(ev.From) + " " + text
	case chat.KindSystem:
		head = ts + f.st(th.Dim, "-- "+ev.Text)
	case chat.KindDMIn:
		head = ts + f.st(th.DM, "[私信 DM ← "+ev.From+"] ") + ev.Text
	case chat.KindDMOut:
		head = ts + f.st(th.DM, "[私信 DM → "+ev.To+"] ") + ev.Text
	case chat.KindInfo:
		head = ts + f.st(th.Info, "* "+ev.Text)
	case chat.KindError:
		head = ts + f.st(th.Error, "! "+ev.Text)
	case chat.KindMention:
		head = ts + f.st(th.Mention, "@ #"+ev.Room+" <"+ev.From+"> ") + " " + ev.Text + f.st(th.Dim, "  · /join "+ev.Room)
	case chat.KindNotice:
		from := "系统 System"
		if ev.From != "" {
			from = "管理员 Admin " + ev.From
		}
		head = ts + f.st(th.Notice, fmt.Sprintf("📢 [%s] %s", from, ev.Text))
	default:
		head = ts + ev.Text
	}
	out := []string{head}
	body := func(l string, en bool) {
		switch {
		case en:
			out = append(out, "        "+f.st(th.Dim, l))
		case ev.Kind == chat.KindInfo || ev.Kind == chat.KindError:
			out = append(out, "        "+f.st(th.Info, l))
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

// highlightTags styles every "@nick" in text, so a mention of someone stands
// out from ordinary words (underlined, not just coloured). "@" inside a word
// (an e-mail address) is left alone.
func highlightTags(text string, p paint) string {
	if strings.IndexByte(text, '@') < 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(text); {
		if text[i] != '@' || (i > 0 && isWordByte(strings.ToLower(text[i-1:i]), 0)) {
			i++
			continue
		}
		j, n := i+1, 0
		for j < len(text) {
			r, sz := utf8.DecodeRuneInString(text[j:])
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') || n == 16 {
				break
			}
			j += sz
			n++
		}
		if n < 2 { // nicknames are at least 2 characters
			i = j
			continue
		}
		b.WriteString(text[last:i])
		b.WriteString(p.R(text[i:j]))
		last, i = j, j
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
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
