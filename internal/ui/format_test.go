package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

func TestMentions(t *testing.T) {
	cases := []struct {
		text, nick string
		want       bool
	}{
		{"hey alice!", "Alice", true},
		{"malice", "alice", false},
		{"alice_bot here", "alice", false},
		{"夜猫子你好", "夜猫子", true},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := Mentions(c.text, c.nick); got != c.want {
			t.Errorf("Mentions(%q,%q)=%v", c.text, c.nick, got)
		}
	}
}

func TestPlainFormatUsesTimezone(t *testing.T) {
	ev := chat.Event{Time: time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC), Kind: chat.KindChat, From: "Bob", Text: "hi Alice"}
	f := Formatter{Self: func() string { return "Alice" }, Loc: func() *time.Location { return time.FixedZone("x", 8*3600) }}
	got := f.Lines(ev)[0]
	if !strings.HasPrefix(got, "[18:30]") || !strings.Contains(got, "» hi Alice") {
		t.Fatalf("got %q", got)
	}
}
