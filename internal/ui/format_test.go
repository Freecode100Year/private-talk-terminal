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

func TestFormatterLanguage(t *testing.T) {
	ev := chat.Event{ID: 987654, Kind: chat.KindSystem, Room: "general", Text: "小明 进入了房间", En: "小明 joined"}
	zh := Formatter{Styled: true}.Lines(ev)[0]
	en := Formatter{Styled: true, Lang: func() string { return "en" }}.Lines(ev)[0]
	if !strings.Contains(zh, "进入了房间") || strings.Contains(zh, "joined") {
		t.Fatalf("zh rendering %q", zh)
	}
	if !strings.Contains(en, "joined") || strings.Contains(en, "进入") {
		t.Fatalf("en rendering %q (render cache must be per language)", en)
	}
	info := chat.Event{Kind: chat.KindInfo, Text: "标题", Lines: []string{"一"}, En: "Title", LinesEn: []string{"one"}}
	if l := (Formatter{Lang: func() string { return "en" }}).Lines(info); !strings.HasSuffix(l[0], "* Title") || !strings.HasSuffix(l[1], "one") {
		t.Fatalf("en info %q", l)
	}
}
