package ui

import (
	"hash/fnv"
	"strings"
	"testing"

	"github.com/Freecode100Year/vivant/internal/chat"
)

func TestWebPalettes(t *testing.T) {
	p := WebPalettes()
	for _, name := range []string{chat.ThemeGreen, chat.ThemeDark, chat.ThemeLight} {
		w, ok := p[name]
		if !ok || len(w.Nicks) != len(themeGreen.Nicks) || !strings.HasPrefix(w.Bg, "#") {
			t.Fatalf("%s: %+v", name, w)
		}
	}
	if p[chat.ThemeGreen].Bg != hex256(194) {
		t.Fatal("green background differs from the terminal theme")
	}
}

func TestNickColorMatchesTerminal(t *testing.T) {
	for _, n := range []string{"Alice", "夜猫子", "Ghost", "anon42"} {
		h := fnv.New32a()
		h.Write([]byte(strings.ToLower(n)))
		if NickColor(n) != int(h.Sum32()%uint32(len(themeGreen.Nicks))) {
			t.Fatalf("%s: colour index differs", n)
		}
	}
}
