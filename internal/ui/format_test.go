package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
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

func TestRings(t *testing.T) {
	cases := []struct {
		ev   chat.Event
		want bool
	}{
		{chat.Event{Kind: chat.KindDMIn, From: "x", Text: "hi"}, true},
		{chat.Event{Kind: chat.KindMention, From: "x", Text: "@me"}, true},
		{chat.Event{Kind: chat.KindChat, From: "x", Text: "hey me!"}, true},
		{chat.Event{Kind: chat.KindChat, From: "x", Text: "hello all"}, false},
		{chat.Event{Kind: chat.KindChat, From: "me", Text: "me talking"}, false},
		{chat.Event{Kind: chat.KindChat, From: "x", Text: "hey me", History: true}, false},
		{chat.Event{Kind: chat.KindSystem, Text: "me joined."}, false},
	}
	for i, c := range cases {
		ev := c.ev
		if got := Rings(&ev, "me"); got != c.want {
			t.Errorf("case %d: Rings=%v want %v", i, got, c.want)
		}
	}
}

func TestFormatterBilingual(t *testing.T) {
	short := chat.Event{ID: 987654, Kind: chat.KindSystem, Room: "general", Text: "小明 进入了房间", En: "joined"}
	if l := (Formatter{Styled: true}).Lines(short); len(l) != 1 || !strings.Contains(l[0], "小明 进入了房间 · joined") {
		t.Fatalf("short pair not joined: %q", l)
	}
	long := chat.Event{Kind: chat.KindInfo, Text: strings.Repeat("中", 30), En: strings.Repeat("e", 40),
		Lines: []string{"一"}, LinesEn: []string{"one"}}
	l := (Formatter{}).Lines(long)
	if len(l) != 4 || !strings.HasSuffix(l[1], strings.Repeat("e", 40)) || !strings.HasSuffix(l[2], "一") || !strings.HasSuffix(l[3], "one") {
		t.Fatalf("long pair layout %q", l)
	}
}

func TestHistoryFromAnotherDayShowsDate(t *testing.T) {
	f := Formatter{}
	old := chat.Event{Kind: chat.KindChat, From: "a", Text: "x", Time: time.Now().Add(-25 * time.Hour), History: true}
	if l := f.Lines(old)[0]; !strings.Contains(l, old.Time.Format("01-02 15:04")) {
		t.Fatalf("no date on old history: %q", l)
	}
	now := chat.Event{Kind: chat.KindChat, From: "a", Text: "x", Time: time.Now()}
	if l := f.Lines(now)[0]; strings.Contains(l, now.Time.Format("01-02")) {
		t.Fatalf("date on a live line: %q", l)
	}
}
