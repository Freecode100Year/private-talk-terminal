package ui

import (
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// sgr is a precomputed "Select Graphic Rendition" prefix. Styling a string is
// a concatenation — no allocation-heavy style engine on the hot path, which
// matters when every message is rendered once per connected session.
type sgr string

// R wraps s in the style and a reset.
func (c sgr) R(s string) string {
	if c == "" {
		return s
	}
	return string(c) + s + "\x1b[m"
}

// Basic 16-colour palette (works on Windows conhost, old xterms, everything).
func fg(n int) sgr {
	if n < 8 {
		return sgr("\x1b[" + itoa(30+n) + "m")
	}
	return sgr("\x1b[" + itoa(90+n-8) + "m")
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

const (
	sBold      sgr = "\x1b[1m"
	sHeader    sgr = "\x1b[1;97;44m"
	sHeaderDim sgr = "\x1b[37;44m"
	sCurRoom   sgr = "\x1b[1;92m"
	sPrompt    sgr = "\x1b[1;92m"
	sSelf      sgr = "\x1b[1;97m"
	sMention   sgr = "\x1b[1;93m"
	sDim       sgr = "\x1b[90m"
	sInfo      sgr = "\x1b[36m"
	sError     sgr = "\x1b[91m"
	sNotice    sgr = "\x1b[93m"
	sDM        sgr = "\x1b[95m"
	sOK        sgr = "\x1b[92m"
)

var nickPalette = []sgr{fg(2), fg(3), fg(4), fg(5), fg(6), fg(12), fg(14), fg(10), fg(11), fg(13)}

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
