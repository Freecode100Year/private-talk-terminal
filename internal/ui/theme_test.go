package ui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
)

// xterm256 returns the standard RGB value of a 256-colour index (16–255;
// 0–15 are user-configurable and deliberately unused by the themes).
func xterm256(n int) (r, g, b float64) {
	switch {
	case n >= 232:
		v := float64(8 + (n-232)*10)
		return v, v, v
	case n >= 16:
		n -= 16
		lv := func(i int) float64 {
			if i == 0 {
				return 0
			}
			return float64(55 + i*40)
		}
		return lv(n / 36), lv(n / 6 % 6), lv(n % 6)
	}
	panic("system colour used in a theme")
}

func luminance(n int) float64 {
	r, g, b := xterm256(n)
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(a, b int) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestThemeContrast(t *testing.T) {
	for _, th := range themes {
		if c := contrast(th.fg, th.bg); c < 15 {
			t.Errorf("%s: body text %.2f:1, want >= 15", th.Name, c)
		}
		styles := map[string]paint{
			"Header": th.Header, "HeaderDim": th.HeaderDim, "CurRoom": th.CurRoom, "Prompt": th.Prompt,
			"Self": th.Self, "Mention": th.Mention, "Tag": th.Tag, "Dim": th.Dim, "Info": th.Info,
			"Error": th.Error, "Notice": th.Notice, "DM": th.DM, "OK": th.OK,
		}
		for i, n := range th.Nicks {
			styles["Nick"+string(rune('0'+i))] = n
		}
		for name, p := range styles {
			bg := p.bg
			if bg < 0 {
				bg = th.bg
			}
			if c := contrast(p.fg, bg); c < 7 {
				t.Errorf("%s/%s: %.2f:1, want >= 7 (AAA)", th.Name, name, c)
			}
			if p.off != th.Base {
				t.Errorf("%s/%s does not return to the theme base", th.Name, name)
			}
		}
		// the mention band must stand out from the page by brightness too,
		// not only by hue
		if c := contrast(th.Mention.bg, th.bg); c < 2 {
			t.Errorf("%s: mention band vs background %.2f:1, too close", th.Name, c)
		}
	}
}

func TestParseThemeMatchesUI(t *testing.T) {
	for _, in := range []string{"black", "white", "green", "护眼", "dark", "light"} {
		name, ok := chat.ParseTheme(in)
		if !ok || themeByName(name).Name != name {
			t.Errorf("theme %q -> %q, %v", in, name, ok)
		}
	}
}

func TestHighlightTags(t *testing.T) {
	p := paint{on: "<", off: ">"}
	cases := map[string]string{
		"@Antigravity 你在做什么": "<@Antigravity> 你在做什么",
		"hi @徐徐 and @bob_2!": "hi <@徐徐> and <@bob_2>!",
		"mail a@b.com":       "mail a@b.com",
		"@ x @y":             "@ x @y",
		"no tags":            "no tags",
	}
	for in, want := range cases {
		if got := highlightTags(in, p); got != want {
			t.Errorf("highlightTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestThemedRendering(t *testing.T) {
	ev := chat.Event{ID: 424242, Time: time.Now(), Kind: chat.KindChat, From: "bob", Text: "hey @carol"}
	dark := Formatter{Styled: true, Theme: themeDark}.Lines(ev)[0]
	light := Formatter{Styled: true, Theme: themeLight}.Lines(ev)[0]
	if dark == light {
		t.Fatal("render cache shared lines across themes")
	}
	if !strings.Contains(light, themeLight.Tag.R("@carol")) || strings.Contains(light, "\x1b[m") {
		t.Fatalf("light line %q", light)
	}
	me := Formatter{Styled: true, Theme: themeGreen, Self: func() string { return "carol" }}.Lines(ev)[0]
	if !strings.Contains(me, themeGreen.Mention.R("» hey @carol ")) {
		t.Fatalf("mention band missing: %q", me)
	}
}

func TestActiveStyle(t *testing.T) {
	b := themeDark.Base
	m := themeDark.Mention
	if got := activeStyle("x"+m.on+"band", b); got != m.on {
		t.Errorf("open band: %q", got)
	}
	if got := activeStyle(m.R("band")+" tail", b); got != "" {
		t.Errorf("closed band: %q", got)
	}
}
