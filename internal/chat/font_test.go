package chat

import "testing"

func TestParseFont(t *testing.T) {
	for _, tc := range []struct {
		cur  int
		arg  string
		want int
		ok   bool
	}{
		{0, "+", FontDefault + fontStep, true},
		{0, "-", FontDefault - fontStep, true},
		{20, "大", 22, true},
		{20, "smaller", 18, true},
		{20, "reset", FontDefault, true},
		{20, "24px", 24, true},
		{20, "999", FontMax, true},
		{20, "3", FontMin, true},
		{FontMax, "+", FontMax, true},
		{FontMin, "-", FontMin, true},
		{20, "huge-ish", 20, false},
	} {
		got, ok := ParseFont(tc.cur, tc.arg)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseFont(%d, %q) = %d, %v; want %d, %v", tc.cur, tc.arg, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFontCommand(t *testing.T) {
	h := newTestHub()
	ssh, _ := h.Connect("ip-a", false)
	web, _ := h.Connect("ip-b", false)
	h.SetWeb(web)
	drain(ssh)
	drain(web)

	if h.Status(web).Font != FontDefault || !h.Status(web).Web || h.Status(ssh).Web {
		t.Fatal("bad defaults")
	}
	// SSH: the terminal owns the font; explain instead of pretending.
	h.Handle(ssh, "/font +")
	if evs := drain(ssh); !has(evs, KindInfo, "SSH app owns the font size") {
		t.Fatalf("ssh /font: %+v", evs)
	}
	if h.Status(ssh).Font != FontDefault {
		t.Fatal("ssh font changed")
	}
	// Web: the size is stored and reported through Status.
	h.Handle(web, "/font +")
	if evs := drain(web); !has(evs, KindInfo, "18px") || h.Status(web).Font != 18 {
		t.Fatalf("web /font +: %+v, %d", evs, h.Status(web).Font)
	}
	h.Handle(web, "/字号 30")
	if h.Status(web).Font != 30 {
		t.Fatal("/字号 30")
	}
	h.Handle(web, "/font banana")
	if evs := drain(web); !has(evs, KindError, "Usage: /font") || h.Status(web).Font != 30 {
		t.Fatal("bad argument should keep the size")
	}
	h.SetFont(web, "-")
	h.SetFont(web, "nonsense")
	if evs := drain(web); h.Status(web).Font != 28 || len(evs) != 0 { // silent
		t.Fatalf("SetFont: %d, %+v", h.Status(web).Font, evs)
	}
}
