package chat

import (
	"strings"
	"testing"
)

func TestFindInvite(t *testing.T) {
	inv, ok := FindInvite("来这里 " + sampleWrapped + " 口令另发")
	if !ok || inv == nil || !inv.Encrypted || inv.Title != "夜聊" || inv.Host != "hr50.freedom8964.com" {
		t.Fatalf("wrapped: %+v ok=%v", inv, ok)
	}
	inv, ok = FindInvite(sampleSecret)
	if !ok || inv == nil || inv.Encrypted {
		t.Fatalf("secret link must be recognised as NOT protected: %+v", inv)
	}
	if inv, ok := FindInvite(sampleNostr); !ok || inv == nil || !inv.Encrypted {
		t.Fatalf("nostr wrapped: %+v", inv)
	}
	if inv, ok := FindInvite("just text"); !ok || inv != nil {
		t.Fatal("plain text treated as invite")
	}
	if _, ok := FindInvite("https://x.example/#hr1=!!!notbase64"); ok {
		t.Fatal("malformed invite accepted")
	}
	// tampering: a config claiming encrypted but still carrying the master key
	if inv, ok := FindInvite("https://evil.example/#hr1=eyJ2ZXJzaW9uIjoyLCJyb29tSWQiOiJhYmMiLCJlbmNyeXB0ZWQiOnRydWUsIm1hc3RlciI6IjAwIiwiY2lwaGVydGV4dCI6IjAwIn0"); !ok || inv == nil || inv.Encrypted {
		t.Fatalf("config with a raw master key must count as unprotected: %+v", inv)
	}
}

func TestInvitePolicy(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	drain(a)
	drain(b)

	h.Handle(a, "进来聊 "+sampleSecret)
	if !has(drain(a), KindError, "已拦截") {
		t.Fatal("secret link not blocked")
	}
	for _, e := range drain(b) {
		if strings.Contains(e.Text, "#hr1=") {
			t.Fatal("secret link reached another user")
		}
	}
	h.Handle(a, "/msg "+h.Nick(b)+" "+sampleSecret)
	for _, e := range drain(b) {
		if strings.Contains(e.Text, "#hr1=") {
			t.Fatal("secret link delivered by DM")
		}
	}

	h.Handle(a, "进来聊 "+sampleWrapped)
	var got *Event
	for _, e := range drain(b) {
		if e.Invite != nil {
			e := e
			got = &e
		}
	}
	if got == nil || !strings.Contains(got.Text, sampleWrapped) || !got.Invite.Trusted || got.Invite.N != 1 {
		t.Fatalf("protected invite not delivered intact (len %d > 500 cap): %+v", len(sampleWrapped), got)
	}
	// ordinary messages still capped
	h.Handle(a, strings.Repeat("长", 900))
	for _, e := range drain(b) {
		if e.Kind == KindChat && len([]rune(e.Text)) > 500 {
			t.Fatal("non-invite message not capped")
		}
	}
	// invites to other hosts are marked untrusted
	h2 := NewHub(Config{SecureURL: "https://other.example/", MsgBurst: 100, MsgPerSecond: 100})
	x, _ := h2.Connect("x", false)
	y, _ := h2.Connect("y", false)
	drain(y)
	h2.Handle(x, sampleWrapped)
	for _, e := range drain(y) {
		if e.Invite != nil && e.Invite.Trusted {
			t.Fatal("invite to a non-configured host marked trusted")
		}
	}
}

func TestSecureNeverCreatesKeys(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	drain(a)
	h.Handle(a, "/secure")
	evs := drain(a)
	if len(evs) != 1 || !strings.Contains(strings.Join(evs[0].Lines, "\n"), "https://hr50.freedom8964.com/") {
		t.Fatalf("/secure output: %+v", evs)
	}
	if strings.Contains(strings.Join(evs[0].Lines, "\n"), "#hr1=") {
		t.Fatal("/secure must not hand out a room link — the server must never create keys")
	}
}
