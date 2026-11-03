package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize removes anything that could manipulate a terminal or spoof the
// display: C0/C1 control characters (which kills every ANSI/OSC escape
// sequence, because they all start with ESC 0x1b or CSI 0x9b), DEL, and
// Unicode bidi overrides/isolates. Tabs become spaces. Invalid UTF-8 is
// dropped. The result is trimmed and capped at maxRunes runes.
func Sanitize(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r == utf8.RuneError && size <= 1 {
			continue
		}
		if r == '\t' {
			r = ' '
		}
		if !Printable(r) {
			continue
		}
		if maxRunes > 0 && n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// Printable reports whether r is safe to show in a terminal: no C0/C1
// controls (which covers every escape sequence), DEL, bidi controls or BOM.
func Printable(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return false
	case isBidiControl(r), r == 0xfeff, r == utf8.RuneError:
		return false
	}
	return true
}

func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e: // LRE RLE PDF LRO RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI RLI FSI PDI
		return true
	case r == 0x200e || r == 0x200f || r == 0x061c: // LRM RLM ALM
		return true
	}
	return false
}

// ValidNick reports whether a nickname is acceptable: 2–16 runes made of
// letters (any script, so 中文昵称 works), digits, '_' and '-'.
func ValidNick(n string) bool {
	c := utf8.RuneCountInString(n)
	if c < 2 || c > 16 {
		return false
	}
	for _, r := range n {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

var reservedNicks = map[string]bool{
	"system": true, "admin": true, "administrator": true, "root": true,
	"you": true, "server": true, "moderator": true, "mod": true,
	"guest": true, "plain": true, "anonymous": true,
}

func reservedNick(n string) bool { return reservedNicks[strings.ToLower(n)] }

// ValidRoomName: 2–24 runes, lowercase letters/digits/'-'/'_'.
func ValidRoomName(n string) bool {
	c := utf8.RuneCountInString(n)
	if c < 2 || c > 24 {
		return false
	}
	for _, r := range n {
		if !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '_' || r == '-' || (unicode.IsLetter(r) && !unicode.IsUpper(r))) {
			return false
		}
	}
	return true
}
