// Package ui renders chat events for SSH sessions: a full Bubble Tea TUI and
// a plain line-mode fallback for old or minimal terminals.
package ui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

// Palette used by the TUI. Plain ANSI 16-colour indexes so it works on
// Windows conhost, old xterms and everything in between.
var (
	cTime    = lipgloss.Color("8")
	cSystem  = lipgloss.Color("8")
	cInfo    = lipgloss.Color("6")
	cError   = lipgloss.Color("9")
	cNotice  = lipgloss.Color("11")
	cDM      = lipgloss.Color("13")
	cSelf    = lipgloss.Color("15")
	cAccent  = lipgloss.Color("10")
	nickPals = []string{"2", "3", "4", "5", "6", "12", "14", "10", "11", "13"}
)

func nickColor(n string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(n)))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(nickPals[h.Sum32()%uint32(len(nickPals))]))
}

// Formatter turns events into display lines.
type Formatter struct {
	Styled bool
	Self   func() string         // current nickname of the viewer
	Loc    func() *time.Location // viewer's timezone (nil = server local)
}

func (f Formatter) st(c interface{ RGBA() (r, g, b, a uint32) }, s string) string {
	if !f.Styled {
		return s
	}
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

// Lines renders one event to one or more unwrapped lines.
func (f Formatter) Lines(ev chat.Event) []string {
	if ev.History && f.Styled { // replayed history: whole line dimmed
		lines := Formatter{Self: f.Self, Loc: f.Loc}.Lines(ev)
		faint := lipgloss.NewStyle().Foreground(cTime)
		for i := range lines {
			lines[i] = faint.Render(lines[i])
		}
		return lines
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
	ts := "[" + t.Format("15:04") + "] "
	if f.Styled {
		ts = f.st(cTime, ts)
	}
	self := ""
	if f.Self != nil {
		self = f.Self()
	}
	nick := func(n string) string {
		if !f.Styled {
			return n
		}
		if n == self {
			return lipgloss.NewStyle().Foreground(cSelf).Bold(true).Render(n)
		}
		return nickColor(n).Render(n)
	}

	text := ev.Text
	if (ev.Kind == chat.KindChat || ev.Kind == chat.KindAction) && Mentions(text, self) && ev.From != self {
		if f.Styled {
			text = lipgloss.NewStyle().Foreground(cNotice).Bold(true).Render(text)
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
		head = ts + f.st(cSystem, "-- "+ev.Text)
	case chat.KindDMIn:
		head = ts + f.st(cDM, "[私信 from "+ev.From+"] ") + ev.Text
	case chat.KindDMOut:
		head = ts + f.st(cDM, "[私信 to "+ev.To+"] ") + ev.Text
	case chat.KindInfo:
		head = ts + f.st(cInfo, "* "+ev.Text)
	case chat.KindError:
		head = ts + f.st(cError, "! "+ev.Text)
	case chat.KindNotice:
		from := "System"
		if ev.From != "" {
			from = "Admin " + ev.From
		}
		head = ts + f.st(cNotice, fmt.Sprintf("📢 [%s] %s", from, ev.Text))
	default:
		head = ts + ev.Text
	}
	out := []string{head}
	for _, l := range ev.Lines {
		if ev.Kind == chat.KindInfo || ev.Kind == chat.KindError {
			out = append(out, "        "+f.st(cInfo, l))
		} else {
			out = append(out, "        "+l)
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
