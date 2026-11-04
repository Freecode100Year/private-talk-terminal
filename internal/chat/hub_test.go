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
		if e.Kind == kind && (strings.Contains(e.Text, substr) || strings.Contains(e.En, substr)) {
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

func TestUnreadCounts(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(b, "/join chinese")
	h.Handle(b, "一")
	h.Handle(b, "二")
	unread := func(c *Client, room string) RoomInfo {
		for _, r := range h.Status(c).Rooms {
			if r.Name == room {
				return r
			}
		}
		t.Fatalf("room %s missing", room)
		return RoomInfo{}
	}
	if got := unread(a, "chinese").Unread; got != 2 {
		t.Fatalf("unread = %d, want 2", got)
	}
	if got := unread(b, "chinese").Unread; got != 0 {
		t.Fatalf("current room shows unread %d", got)
	}
	h.Handle(a, "/join chinese")
	h.Handle(a, "/leave")
	if got := unread(a, "chinese").Unread; got != 0 {
		t.Fatalf("visiting a room should clear unread, got %d", got)
	}
	h.Handle(b, "三")
	if got := unread(a, "chinese").Unread; got != 1 {
		t.Fatalf("unread after leaving = %d, want 1", got)
	}
}

func TestCrossRoomMention(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/nick 夜猫子")
	h.Handle(b, "/nick Bob")
	h.Handle(b, "/join chinese")
	drain(a)
	h.Handle(b, "@夜猫子你好，来中文频道")
	evs := drain(a)
	if !has(evs, KindMention, "@夜猫子你好") {
		t.Fatalf("mention without trailing space not delivered: %+v", evs)
	}
	for _, r := range h.Status(a).Rooms {
		if r.Name == "chinese" && !r.Mentioned {
			t.Fatal("room not flagged as mentioned")
		}
	}
	// people in the same room just see the message, no extra notice
	h.Handle(a, "/join chinese")
	drain(a)
	h.Handle(b, "@夜猫子 again")
	if has(drain(a), KindMention, "again") {
		t.Fatal("mention notice sent to someone already in the room")
	}
	h.Handle(a, "/leave")
	// ignoring the sender silences mentions too
	h.Handle(a, "/ignore Bob")
	drain(a)
	h.Handle(b, "@夜猫子 hello?")
	if has(drain(a), KindMention, "hello?") {
		t.Fatal("mention from an ignored user delivered")
	}
}

func TestMentionNeverLeaksPrivateRoom(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "/nick Alice")
	h.Handle(b, "/create")
	drain(a)
	h.Handle(b, "@Alice secret room talk")
	if has(drain(a), KindMention, "secret") {
		t.Fatal("mention from a private room revealed it to an outsider")
	}
}

func TestBanListUnbanAndPersistAPI(t *testing.T) {
	h := newTestHub()
	adm, _ := h.Connect("ip-admin", true)
	bad, _ := h.Connect("deadbeefcafe0001", false)
	_, v0 := h.Bans()
	h.Handle(adm, "/ban "+h.Nick(bad)+" 30")
	bans, v1 := h.Bans()
	if len(bans) != 1 || v1 == v0 {
		t.Fatalf("ban not recorded/versioned: %v %d %d", bans, v0, v1)
	}
	drain(adm)
	h.Handle(adm, "/bans")
	if !has(drain(adm), KindInfo, "1 active bans") {
		t.Fatal("/bans listing missing")
	}
	h.Handle(adm, "/unban deadbeef")
	if bans, _ := h.Bans(); len(bans) != 0 {
		t.Fatal("/unban failed")
	}
	// restore after a "restart"
	h2 := newTestHub()
	if n := h2.LoadBans(map[string]time.Time{"k1": time.Now().Add(time.Hour), "old": time.Now().Add(-time.Hour), OnionKey: time.Now().Add(time.Hour)}); n != 1 {
		t.Fatalf("LoadBans loaded %d, want 1 (expired and onion skipped)", n)
	}
	if _, err := h2.Connect("k1", false); err != ErrBanned {
		t.Fatalf("restored ban not enforced: %v", err)
	}
}

func TestForgetErasesOwnHistoryOnly(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	b, _ := h.Connect("ip-b", false)
	h.Handle(a, "secret from a")
	h.Handle(b, "hello from b")
	h.Handle(a, "/join chinese")
	h.Handle(a, "another secret")
	drain(a)
	h.Handle(a, "/forget")
	if !has(drain(a), KindInfo, "删除你的 2 条消息") {
		t.Fatal("forget did not report 2 erased messages")
	}
	c, _ := h.Connect("ip-c", false)
	evs := drain(c)
	for _, e := range evs {
		if e.History && strings.Contains(e.Text, "secret") {
			t.Fatal("forgotten message replayed to a newcomer")
		}
	}
	if !has(evs, KindChat, "hello from b") {
		t.Fatal("other people's messages were erased too")
	}
	h.mu.Lock()
	for _, r := range h.rooms {
		for _, e := range r.history[len(r.history):cap(r.history)] {
			if strings.Contains(e.Text, "secret") {
				h.mu.Unlock()
				t.Fatal("erased text still in the history backing array")
			}
		}
	}
	h.mu.Unlock()
}

func TestPrivacyReflectsConfig(t *testing.T) {
	h := NewHub(Config{HistorySize: 20, HistoryTTL: 30 * time.Minute, PersistBans: true, TorEnabled: true})
	a, _ := h.Connect("ip-a", false)
	drain(a)
	h.Handle(a, "/privacy")
	evs := drain(a)
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	all := strings.Join(evs[0].Lines, "\n")
	for _, want := range []string{"最多 20 条", "30m0s", "已开启持久化", ".onion", "不写入日志", "能看到连接来源 IP"} {
		if !strings.Contains(all, want) {
			t.Errorf("/privacy missing %q:\n%s", want, all)
		}
	}
	h2 := newTestHub()
	b, _ := h2.Connect("ip-b", false)
	drain(b)
	h2.Handle(b, "/privacy")
	all = strings.Join(drain(b)[0].Lines, "\n")
	if !strings.Contains(all, "仅在内存") || !strings.Contains(all, "未开启") {
		t.Errorf("defaults not reported:\n%s", all)
	}
}

func TestHelpIsShortByDefault(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("ip-a", false)
	drain(a)
	h.Handle(a, "/help")
	short := drain(a)
	if len(short) != 1 || len(short[0].Lines) > 8 || !strings.Contains(strings.Join(short[0].Lines, "\n"), "/help all") {
		t.Fatalf("default help should be a short list pointing to /help all: %+v", short)
	}
	h.Handle(a, "/help all")
	all := strings.Join(drain(a)[0].Lines, "\n")
	for _, cmd := range []string{"/secure", "/forget", "/privacy", "/ignore", "/create", "/tz"} {
		if !strings.Contains(all, cmd) {
			t.Errorf("/help all missing %s", cmd)
		}
	}
	if strings.Contains(all, "/kick") {
		t.Error("admin commands shown to a non-admin")
	}
}

func TestPresenceSummaryCounts(t *testing.T) {
	h := NewHub(Config{QuietAbove: 1})
	a, _ := h.Connect("a", false)
	h.Connect("b", false) // a alone -> announced
	drain(a)
	c, _ := h.Connect("c", false)
	h.Connect("d", false)
	h.Disconnect(c)
	h.FlushPresence()
	if !has(drain(a), KindSystem, "2 人进入、1 人离开") {
		t.Fatal("summary should count 2 joins and 1 leave")
	}
}

func TestModerationNeverCollapsed(t *testing.T) {
	h := NewHub(Config{QuietAbove: 1})
	adm, _ := h.Connect("adm", true)
	h.Connect("x", false)
	v, _ := h.Connect("v", false)
	drain(adm)
	h.Handle(adm, "/kick "+h.Nick(v))
	if !has(drain(adm), KindSystem, "was kicked") {
		t.Fatal("kicks must stay visible even in busy rooms")
	}
}

func TestBellToggle(t *testing.T) {
	h := newTestHub()
	a, _ := h.Connect("a", false)
	if !h.BellOn(a) || !h.Status(a).Bell {
		t.Fatal("bell should default to on")
	}
	h.Handle(a, "/bell off")
	if h.BellOn(a) {
		t.Fatal("/bell off ignored")
	}
	h.Handle(a, "/bell on")
	if !h.BellOn(a) {
		t.Fatal("/bell on ignored")
	}
}

func TestLanguage(t *testing.T) {
	for in, want := range map[string]string{"zh_CN.UTF-8": "zh", "zh_TW": "zh", "en_US.UTF-8": "en", "de_DE": "en", "C.UTF-8": "zh", "": "zh", "POSIX": "zh"} {
		if got := normLang(in, "zh"); got != want {
			t.Errorf("normLang(%q) = %q, want %q", in, got, want)
		}
	}
	if normLang("C", "en") != "en" {
		t.Error("server default not used for C locale")
	}
	h := newTestHub()
	a, _ := h.ConnectLang("a", false, "en_GB.UTF-8")
	b, _ := h.Connect("b", false)
	if h.Lang(a) != "en" || h.Lang(b) != "zh" {
		t.Fatalf("langs %q %q", h.Lang(a), h.Lang(b))
	}
	evs := drain(a)
	if len(evs) == 0 || evs[0].En == "" || !strings.Contains(evs[0].En, "Welcome") || !strings.Contains(evs[0].Text, "欢迎") {
		t.Fatalf("welcome not bilingual: %+v", evs)
	}
	drain(b)
	h.Handle(b, "/lang en")
	if h.Lang(b) != "en" {
		t.Fatal("/lang en had no effect")
	}
	h.Handle(b, "/lang zh")
	if h.Lang(b) != "zh" {
		t.Fatal("/lang zh had no effect")
	}
	// every server message to a client carries both languages
	drain(a)
	for _, cmd := range []string{"/help", "/help all", "/rooms", "/who", "/privacy", "/secure", "/bell", "/tz", "/join nope", "/nick x", "/frobnicate"} {
		h.Handle(a, cmd)
		for _, ev := range drain(a) {
			if ev.Kind != KindChat && ev.Kind != KindAction && ev.En == "" {
				t.Errorf("%s: event without English text: %q", cmd, ev.Text)
			}
			if len(ev.Lines) > 0 && len(ev.LinesEn) == 0 {
				t.Errorf("%s: lines without English: %q", cmd, ev.Text)
			}
		}
	}
}
