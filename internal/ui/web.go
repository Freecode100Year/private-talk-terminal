package ui

import "strings"

// WebColors is a theme's palette as CSS colours, for the web page. It is
// derived from the same xterm-256 definitions as the terminal themes, so the
// contrast guarantees checked by TestThemeContrast hold in the browser too.
type WebColors struct {
	Name                  string
	Fg, Bg                string
	HeaderFg, HeaderBg    string
	HeaderDim             string
	CurRoom, Prompt, Self string
	MentionFg, MentionBg  string
	Tag, Dim, Info, Error string
	Notice, DM, OK        string
	Nicks                 []string
}

func (p paint) fgHex() string { return hex256(p.fg) }

// WebPalettes returns every theme, keyed by its name (chat.ThemeGreen, …).
func WebPalettes() map[string]WebColors {
	out := map[string]WebColors{}
	for _, t := range themes {
		w := WebColors{
			Name: t.Name, Fg: hex256(t.fg), Bg: hex256(t.bg),
			HeaderFg: t.Header.fgHex(), HeaderBg: hex256(t.Header.bg), HeaderDim: t.HeaderDim.fgHex(),
			CurRoom: t.CurRoom.fgHex(), Prompt: t.Prompt.fgHex(), Self: t.Self.fgHex(),
			MentionFg: t.Mention.fgHex(), MentionBg: hex256(t.Mention.bg),
			Tag: t.Tag.fgHex(), Dim: t.Dim.fgHex(), Info: t.Info.fgHex(), Error: t.Error.fgHex(),
			Notice: t.Notice.fgHex(), DM: t.DM.fgHex(), OK: t.OK.fgHex(),
		}
		for _, n := range t.Nicks {
			w.Nicks = append(w.Nicks, n.fgHex())
		}
		out[t.Name] = w
	}
	return out
}

// NickColor is the index into Nicks used for nickname n (same as the
// terminal UI, so a person has the same colour everywhere).
func NickColor(n string) int {
	return int(fnv32a(strings.ToLower(n)) % uint32(len(themeGreen.Nicks)))
}

func fnv32a(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
