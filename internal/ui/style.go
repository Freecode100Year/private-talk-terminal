package ui

import (
	"strconv"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/Freecode100Year/vivant/internal/chat"
)

// Colours are chosen for low vision and colour blindness:
//
//   - Every theme paints its own background, so contrast is known rather than
//     guessed. Every colour — body text, nicknames, timestamps, hints, errors —
//     is at least 7:1 against it (WCAG AAA); body text is 15:1 or more.
//     TestThemeContrast enforces this.
//   - Nickname and status hues come from the Okabe–Ito colour-blind-safe
//     family (orange, sky blue, bluish green, yellow, blue, vermillion,
//     reddish purple). Nothing relies on telling red from green.
//   - Meaning never rides on colour alone: a line that mentions you is a solid
//     band with a "»" marker, @nick tags are underlined, errors start with "!",
//     the current room with "▸", unread counts with "+" and "@".
//
// Only the xterm 256-colour palette is used (no 24-bit colour), so Windows
// conhost, macOS Terminal.app, tmux and phone SSH apps all show the same thing.

// paint is a precomputed "Select Graphic Rendition" prefix plus the sequence
// that returns to the theme's base colours. Styling a string is a
// concatenation — no allocation-heavy style engine on the hot path, which
// matters when every message is rendered once per connected session.
type paint struct {
	on, off string
	fg, bg  int // xterm-256 indices; bg < 0 means the theme background
}

// R wraps s in the style, then returns to the theme's base colours.
func (p paint) R(s string) string {
	if p.on == "" {
		return s
	}
	return p.on + s + p.off
}

// Theme is a complete colour scheme. Base sets the theme's foreground and
// background; it is written wherever a plain reset would be, so the
// background stays painted.
type Theme struct {
	Name     string
	fg, bg   int
	Base     string
	Defaults string // sets the terminal's default colours to the theme's

	Header, HeaderDim paint // top bar
	CurRoom, Prompt   paint
	Self              paint // your own nickname
	Mention           paint // a line that mentions you: solid band
	Tag               paint // @nick inside a message
	Dim               paint // timestamps, hints, English half, history
	Info, Error       paint
	Notice, DM, OK    paint
	Nicks             []paint
}

func newTheme(name string, fg, bg int) *Theme {
	return &Theme{Name: name, fg: fg, bg: bg,
		Base: "\x1b[0;38;5;" + strconv.Itoa(fg) + ";48;5;" + strconv.Itoa(bg) + "m",
		// OSC 10/11 also set the terminal's own default colours, so cells we
		// never paint — the window padding, columns beyond the size the
		// client reported, rows ConPTY re-renders without our background on
		// Windows — show the theme too. Terminals without it ignore it.
		Defaults: "\x1b]10;" + hex256(fg) + "\x07\x1b]11;" + hex256(bg) + "\x07"}
}

// resetDefaults restores the terminal's own default colours (OSC 110/111).
const resetDefaults = "\x1b]110\x07\x1b]111\x07"

// xterm256 returns the standard RGB value of a 256-colour index (16–255;
// 0–15 are user-configurable and deliberately unused by the themes).
func xterm256(n int) (r, g, b int) {
	switch {
	case n >= 232:
		v := 8 + (n-232)*10
		return v, v, v
	case n >= 16:
		n -= 16
		lv := func(i int) int {
			if i == 0 {
				return 0
			}
			return 55 + i*40
		}
		return lv(n / 36), lv(n / 6 % 6), lv(n % 6)
	}
	panic("system colour used in a theme")
}

// hex256 formats a 256-colour index as #rrggbb.
func hex256(n int) string {
	const d = "0123456789abcdef"
	r, g, b := xterm256(n)
	return string([]byte{'#', d[r>>4], d[r&15], d[g>>4], d[g&15], d[b>>4], d[b&15]})
}

// p builds a style: foreground fg, background bg (-1 = theme's), plus SGR
// attributes such as "1" (bold) or "1;4" (bold underline).
func (t *Theme) p(fg, bg int, attr string) paint {
	on := "\x1b["
	if attr != "" {
		on += attr + ";"
	}
	on += "38;5;" + strconv.Itoa(fg)
	if bg >= 0 {
		on += ";48;5;" + strconv.Itoa(bg)
	}
	return paint{on: on + "m", off: t.Base, fg: fg, bg: bg}
}

func (t *Theme) nicks(fgs ...int) {
	for _, f := range fgs {
		t.Nicks = append(t.Nicks, t.p(f, -1, ""))
	}
}

// Dark: white on black.
var themeDark = func() *Theme {
	t := newTheme(chat.ThemeDark, 231, 16)
	t.Header = t.p(231, 18, "1")
	t.HeaderDim = t.p(255, 18, "")
	t.CurRoom = t.p(117, -1, "1")
	t.Prompt = t.p(117, -1, "1")
	t.Self = t.p(231, -1, "1")
	t.Mention = t.p(16, 208, "1")
	t.Tag = t.p(213, -1, "1;4")
	t.Dim = t.p(250, -1, "")
	t.Info = t.p(116, -1, "")
	t.Error = t.p(210, -1, "1")
	t.Notice = t.p(222, -1, "")
	t.DM = t.p(183, -1, "1")
	t.OK = t.p(117, -1, "")
	//        orange sky  b-green yellow blue verm. purple teal  khaki lilac
	t.nicks(215, 117, 79, 227, 111, 209, 176, 152, 186, 147)
	return t
}()

// Light: black on white.
var themeLight = func() *Theme {
	t := newTheme(chat.ThemeLight, 16, 231)
	lightCommon(t)
	return t
}()

// Green: the "eye-care" pale green background (护眼绿), black text. Default.
var themeGreen = func() *Theme {
	t := newTheme(chat.ThemeGreen, 16, 194)
	lightCommon(t)
	return t
}()

// lightCommon fills in the styles shared by the two light backgrounds; the
// colours are dark enough to pass on the (slightly darker) green as well.
func lightCommon(t *Theme) {
	t.Header = t.p(231, 18, "1")
	t.HeaderDim = t.p(255, 18, "")
	t.CurRoom = t.p(19, -1, "1")
	t.Prompt = t.p(19, -1, "1")
	t.Self = t.p(16, -1, "1")
	t.Mention = t.p(16, 208, "1")
	t.Tag = t.p(90, -1, "1;4")
	t.Dim = t.p(238, -1, "")
	t.Info = t.p(22, -1, "")
	t.Error = t.p(88, -1, "1")
	t.Notice = t.p(52, -1, "1")
	t.DM = t.p(55, -1, "1")
	t.OK = t.p(19, -1, "")
	// Only dark shades reach 7:1 on a light page.
	//       navy red green purple maroon blue violet wine indigo grape
	t.nicks(18, 88, 22, 90, 52, 20, 54, 89, 55, 56)
}

// Themes in /theme cycling order, the default first.
var themes = []*Theme{themeGreen, themeDark, themeLight}

// themeByName returns the named theme (the default for unknown names).
func themeByName(name string) *Theme {
	for _, t := range themes {
		if t.Name == name {
			return t
		}
	}
	return themeGreen
}

// width is the display width of s in terminal cells, ignoring escape
// sequences. Pure ASCII (the common case) is counted without grapheme
// segmentation.
func width(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= utf8.RuneSelf {
			return ansi.StringWidth(s)
		}
		if b == 0x1b { // skip CSI ... final / OSC ... BEL
			if i+1 < len(s) && s[i+1] == '[' {
				i += 2
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				continue
			}
			return ansi.StringWidth(s)
		}
		if b >= 0x20 && b != 0x7f {
			n++
		}
	}
	return n
}

// truncate cuts s to at most w cells (escape sequences preserved).
func truncate(s string, w int, tail string) string {
	if width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, tail)
}
