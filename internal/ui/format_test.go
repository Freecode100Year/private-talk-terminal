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

func TestInviteRendering(t *testing.T) {
	inv := &chat.Invite{URL: "https://hr50.freedom8964.com/#hr1=abcdefghijklmnopqrstuvwxyz", Host: "hr50.freedom8964.com", Title: "夜聊", Encrypted: true, Trusted: true, N: 7}
	ev := chat.Event{Time: time.Now(), Kind: chat.KindChat, From: "Bob", Text: "来 " + inv.URL, Invite: inv}
	styled := Formatter{Styled: true}.Lines(ev)[0]
	if !strings.Contains(styled, "\x1b]8;;"+inv.URL) || !strings.Contains(styled, "/link 7") || width(styled) > 120 {
		t.Fatalf("styled invite: %q (width %d)", styled, width(styled))
	}
	plain := Formatter{}.Lines(ev)[0]
	if !strings.Contains(plain, inv.URL) || strings.Contains(plain, "\x1b") {
		t.Fatalf("plain invite must keep the raw URL and no escapes: %q", plain)
	}
	inv.Trusted = false
	if s := (Formatter{Styled: true}).Lines(ev)[0]; !strings.Contains(s, "非默认站点") {
		t.Fatal("untrusted host not flagged")
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
