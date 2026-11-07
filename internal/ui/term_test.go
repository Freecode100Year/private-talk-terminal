package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Freecode100Year/vivant/internal/chat"
)

func decode(t *testing.T, in string) []key {
	t.Helper()
	ch := make(chan key, 64)
	stop := make(chan struct{})
	readKeys(strings.NewReader(in), ch, stop)
	close(ch)
	var out []key
	for k := range ch {
		out = append(out, k)
	}
	return out
}

func TestReadKeys(t *testing.T) {
	ks := decode(t, "a中\x1b[A\x1b[5~\x1bOB\r\n\x7f\x1b[200~x\ny\x1b[201~\x03")
	want := []keyKind{kRune, kRune, kUp, kPgUp, kDown, kEnter, kBackspace, kPaste, kCtrlC}
	if len(ks) != len(want) {
		t.Fatalf("got %d keys: %+v", len(ks), ks)
	}
	for i, k := range ks {
		if k.kind != want[i] {
			t.Fatalf("key %d: got %v want %v", i, k.kind, want[i])
		}
	}
	if ks[1].r != '中' || ks[7].text != "x\ny" {
		t.Fatalf("payload wrong: %+v", ks)
	}
	if ks := decode(t, "\x1b"); len(ks) != 1 || ks[0].kind != kEsc {
		t.Fatalf("lone ESC: %+v", ks)
	}
}

type pipeRW struct {
	r   io.Reader
	out bytes.Buffer
}

func (p *pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeRW) Write(b []byte) (int, error) { return p.out.Write(b) }
func (p *pipeRW) String() string              { return p.out.String() }

func TestTermSendsAndRenders(t *testing.T) {
	hub := chat.NewHub(chat.Config{})
	c, _ := hub.Connect("ip", false)
	pr, pw := io.Pipe()
	rw := &pipeRW{r: pr}
	done := make(chan string)
	go func() { done <- RunTerm(rw, hub, c, 80, 24, nil) }()
	io.WriteString(pw, "/nick Tester\r")
	io.WriteString(pw, "hello \x1b]0;evil\x07world\r")
	time.Sleep(200 * time.Millisecond)
	pw.Write([]byte{0x03}) // Ctrl+C
	if msg := <-done; !strings.Contains(msg, "再见") {
		t.Fatalf("exit message %q", msg)
	}
	out := rw.String()
	if !strings.Contains(out, "<") || !strings.Contains(out, "Tester") || !strings.Contains(out, "hello 0;evilworld") {
		t.Fatalf("message not rendered / not sanitized:\n%q", out)
	}
	if strings.Contains(out, "\x1b]0;evil") {
		t.Fatal("escape sequence from user input reached the terminal")
	}
}

func TestTermRingsOnDM(t *testing.T) {
	hub := chat.NewHub(chat.Config{})
	me, _ := hub.Connect("ip-me", false)
	other, _ := hub.Connect("ip-other", false)
	pr, pw := io.Pipe()
	rw := &pipeRW{r: pr}
	done := make(chan string)
	go func() { done <- RunTerm(rw, hub, me, 80, 24, nil) }()
	time.Sleep(100 * time.Millisecond)
	hub.Handle(other, "/msg "+hub.Nick(me)+" ping")
	time.Sleep(150 * time.Millisecond)
	pw.Write([]byte{0x03})
	<-done
	if !strings.Contains(rw.String(), "\a") {
		t.Fatal("no bell on private message")
	}
}

func TestTermHistoryBatch(t *testing.T) {
	tm := &Term{width: 80, height: 24, last: map[int]string{}, th: themeGreen}
	tm.fmt = Formatter{Styled: true, Theme: themeGreen}
	var batch []*chat.Event
	for i := range 700 {
		batch = append(batch, &chat.Event{ID: uint64(i + 1), Kind: chat.KindChat, From: "a", Text: "old", Time: time.Now(), History: true})
	}
	if !tm.addEvent(&chat.Event{Kind: chat.KindHistory, History: true, Batch: batch}) {
		t.Fatal("history should refresh the status rows")
	}
	if len(tm.events) != maxEvents {
		t.Fatalf("kept %d events, want %d", len(tm.events), maxEvents)
	}
	if tm.events[len(tm.events)-1].ID != 700 {
		t.Fatal("newest replayed message not last")
	}
	if !strings.Contains(tm.out.String(), "old") {
		t.Fatal("history not drawn")
	}
}
