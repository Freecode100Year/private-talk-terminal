package chat

import (
	"strings"
	"testing"
	"time"
)

func drain(c *Client) []Event {
	var out []Event
	for {
		select {
		case ev := <-c.Out:
			out = append(out, *ev)
		default:
			return out
		}
	}
}

func has(evs []Event, kind Kind, substr string) bool {
	for _, e := range evs {
		if e.Kind == kind && strings.Contains(e.Text, substr) {
			return true
		}
	}
	return false
}

func newTestHub() *Hub {
	return NewHub(Config{MsgBurst: 100, MsgPerSecond: 100, NickCooldown: time.Millisecond})
}

func TestBroadcastAndRooms(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	drain(a)
	drain(b)

	h.Handle(a, "hello 大家好")
	if evs := drain(b); !has(evs, KindChat, "hello 大家好") {
		t.Fatalf("b did not get message: %+v", evs)
	}

	h.Handle(b, "/join chinese")
	drain(a)
	drain(b)
	h.Handle(a, "only general")
	if evs := drain(b); has(evs, KindChat, "only general") {
		t.Fatal("message leaked across rooms")
	}
	h.Handle(a, "/join nope")
	if evs := drain(a); !has(evs, KindError, "No such room") {
		t.Fatal("expected error for unknown room")
	}
}

func TestHistoryReplay(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	h.Handle(a, "first")
	h.Handle(a, "second")
	b, _ := h.Connect("ip-b", false)
	evs := drain(b)
	n := 0
	for _, e := range evs {
		if e.History && e.Kind == KindChat {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 history events, got %d", n)
	}
}

func TestDMAndReply(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/nick Alice")
	h.Handle(b, "/nick 夜猫子")
	drain(a)
	drain(b)

	h.Handle(a, "/msg 夜猫子 secret")
	if !has(drain(b), KindDMIn, "secret") {
		t.Fatal("DM not delivered")
	}
	if !has(drain(a), KindDMOut, "secret") {
		t.Fatal("DM not echoed")
	}
	h.Handle(b, "/r got it")
	if !has(drain(a), KindDMIn, "got it") {
		t.Fatal("reply not delivered")
	}
}

func TestNickRules(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/nick Ghost")
	drain(a)
	drain(b)
	h.Handle(b, "/nick ghost")
	if !has(drain(b), KindError, "taken") {
		t.Fatal("duplicate nick (case-insensitive) allowed")
	}
	h.Handle(b, "/nick System")
	if !has(drain(b), KindError, "reserved") {
		t.Fatal("reserved nick allowed")
	}
	h.Handle(b, "/nick a")
	if !has(drain(b), KindError, "2–16") {
		t.Fatal("short nick allowed")
	}
	h.Handle(b, "/nick \x1b[31mred")
	if h.Nick(b) == "\x1b[31mred" {
		t.Fatal("escape sequence in nick")
	}
}

func TestSanitizeStripsEscapes(t *testing.T) {
	in := "hi\x1b]0;pwned\x07 \x1b[2J‮evil\u0085 ok"
	got := Sanitize(in, 0)
	if strings.ContainsAny(got, "\x1b\x07‮\u0085") {
		t.Fatalf("control chars survived: %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("content lost: %q", got)
	}
	if n := len([]rune(Sanitize(strings.Repeat("字", 900), 500))); n != 500 {
		t.Fatalf("cap failed: %d", n)
	}
}

func TestFloodProtection(t *testing.T) {
	h := NewHub(Config{MsgBurst: 3, MsgPerSecond: 0.001})
	a, _ := h.Connect("ip-a", false)
	drain(a)
	for i := 0; i < 3; i++ {
		h.Handle(a, "msg "+string(rune('a'+i)))
	}
	h.Handle(a, "one too many")
	if !has(drain(a), KindError, "too fast") {
		t.Fatal("rate limit not applied")
	}
	for i := 0; i < 10; i++ {
		h.Handle(a, "spam "+string(rune('a'+i)))
	}
	if !h.Status(a).Muted {
		t.Fatal("expected auto-mute after repeated violations")
	}
}

func TestRepeatBlocked(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	drain(a)
	h.Handle(a, "same")
	h.Handle(a, "same")
	h.Handle(a, "same")
	if !has(drain(a), KindError, "repeat") {
		t.Fatal("third identical message should be blocked")
	}
}

func TestConnectionLimits(t *testing.T) {
	h := NewHub(Config{MaxConnsPerIP: 2, ConnPerMinute: 100})
	c1, err1 := h.Connect("ip", false)
	_, err2 := h.Connect("ip", false)
	_, err3 := h.Connect("ip", false)
	if err1 != nil || err2 != nil || err3 != ErrTooManyConns {
		t.Fatalf("got %v %v %v", err1, err2, err3)
	}
	h.Disconnect(c1)
	if _, err := h.Connect("ip", false); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}

	h2 := NewHub(Config{MaxConnsPerIP: 100, ConnPerMinute: 2})
	h2.Connect("x", false)
	h2.Connect("x", false)
	if _, err := h2.Connect("x", false); err != ErrConnRate {
		t.Fatalf("want ErrConnRate, got %v", err)
	}
}

func TestPrivateRoomLifecycle(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/create")
	code := h.Status(a).Room
	if !strings.HasPrefix(code, "p-") {
		t.Fatalf("not in private room: %s", code)
	}
	for _, r := range h.Status(b).Rooms {
		if r.Name == code {
			t.Fatal("private room visible to outsiders")
		}
	}
	h.Handle(b, "/join "+code)
	drain(a)
	h.Handle(b, "psst")
	if !has(drain(a), KindChat, "psst") {
		t.Fatal("private room message not delivered")
	}
	h.Handle(a, "/leave")
	h.Handle(b, "/leave")
	h.mu.Lock()
	_, exists := h.rooms[code]
	h.mu.Unlock()
	if exists {
		t.Fatal("empty private room not deleted")
	}
}

func TestAdminKickBan(t *testing.T) {
	h := newTestHub()
	adm, _ := h.Connect("ip-admin", true)
	bad, _ := h.Connect("ip-bad", false)
	h.Handle(bad, "/nick Spammer")
	h.Handle(bad, "/kick Spammer") // non-admin can't
	select {
	case <-bad.Done:
		t.Fatal("non-admin kick worked")
	default:
	}
	h.Handle(adm, "/ban Spammer 5")
	select {
	case <-bad.Done:
	default:
		t.Fatal("ban did not disconnect")
	}
	if _, err := h.Connect("ip-bad", false); err != ErrBanned {
		t.Fatalf("want ErrBanned, got %v", err)
	}
}

func TestHistoryTTL(t *testing.T) {
	h := NewHub(Config{HistoryTTL: 10 * time.Millisecond, MsgBurst: 100, MsgPerSecond: 100})
	a, _ := h.Connect("ip-a", false)
	h.Handle(a, "old news")
	time.Sleep(20 * time.Millisecond)
	h.Janitor()
	b, _ := h.Connect("ip-b", false)
	for _, e := range drain(b) {
		if e.History {
			t.Fatal("expired history replayed")
		}
	}
}

func TestIgnore(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/nick Annoying")
	h.Handle(b, "/ignore Annoying")
	drain(a)
	drain(b)
	h.Handle(a, "blah")
	h.Handle(a, "/msg "+h.Nick(b)+" psst")
	evs := drain(b)
	if has(evs, KindChat, "blah") || has(evs, KindDMIn, "psst") {
		t.Fatal("ignored user's messages delivered")
	}
	if !has(drain(a), KindDMOut, "psst") {
		t.Fatal("sender should not learn they are ignored")
	}
	h.Handle(b, "/unignore Annoying")
	h.Handle(a, "blah again")
	if !has(drain(b), KindChat, "blah again") {
		t.Fatal("unignore failed")
	}
}

func TestReport(t *testing.T) {
	h := newTestHub()
	adm, _ := h.Connect("ip-admin", true)
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(b, "/nick Troll")
	drain(adm)
	h.Handle(a, "/report Troll spamming links")
	evs := drain(adm)
	found := false
	for _, e := range evs {
		if e.Kind == KindNotice && len(e.Lines) == 1 && strings.Contains(e.Lines[0], "Troll") && strings.Contains(e.Lines[0], "spamming") {
			found = true
		}
	}
	if !found {
		t.Fatalf("admin not notified: %+v", evs)
	}
	h.Handle(a, "/reports")
	if has(drain(a), KindInfo, "recent reports") {
		t.Fatal("non-admin can read reports")
	}
}

func TestTimezone(t *testing.T) {
	for in, want := range map[string]int{"+8": 8 * 3600, "-5": -5 * 3600, "+05:30": 5*3600 + 1800, "UTC+9": 9 * 3600, "utc": 0} {
		loc, err := ParseZone(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if _, off := time.Now().In(loc).Zone(); off != want {
			t.Fatalf("%s: offset %d want %d", in, off, want)
		}
	}
	if _, err := ParseZone("../../etc/passwd"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := ParseZone("+99"); err == nil {
		t.Fatal("bad offset accepted")
	}
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	h.Handle(a, "/tz +8")
	if _, off := time.Now().In(h.Loc(a)).Zone(); off != 8*3600 {
		t.Fatal("tz not applied")
	}
}

func TestIdleKick(t *testing.T) {
	h := NewHub(Config{IdleKick: 20 * time.Millisecond})
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	time.Sleep(15 * time.Millisecond)
	h.Handle(b, "/who") // b is active
	time.Sleep(10 * time.Millisecond)
	h.Janitor()
	select {
	case <-a.Done:
	default:
		t.Fatal("idle client not dropped")
	}
	select {
	case <-b.Done:
		t.Fatal("active client dropped")
	default:
	}
}

func TestStatsHasNoIdentities(t *testing.T) {
	h := newTestHub()
	h.Connect("ip-a", false)
	st := h.Stats()
	if st.Online != 1 || st.Rooms != 5 {
		t.Fatalf("%+v", st)
	}
}

func TestOnionBucket(t *testing.T) {
	h := NewHub(Config{MaxConnsPerIP: 2, OnionMaxConns: 5, OnionPerMin: 100})
	for i := 0; i < 5; i++ {
		if _, err := h.Connect(OnionKey, false); err != nil {
			t.Fatalf("onion session %d refused: %v", i, err)
		}
	}
	if _, err := h.Connect(OnionKey, false); err != ErrTooManyConns {
		t.Fatalf("onion cap not enforced: %v", err)
	}
	adm, _ := h.Connect("ip-admin", true)
	h.mu.Lock()
	var victim *Client
	for _, c := range h.clients {
		if c.ipKey == OnionKey {
			victim = c
			break
		}
	}
	h.mu.Unlock()
	h.Handle(adm, "/ban "+h.Nick(victim))
	<-victim.Done
	if _, err := h.Connect(OnionKey, false); err != nil {
		t.Fatalf("banning one Tor user locked out all Tor users: %v", err)
	}
}
