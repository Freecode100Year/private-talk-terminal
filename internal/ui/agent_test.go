package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Freecode100Year/vivant/internal/chat"
)

func TestSplitMessage(t *testing.T) {
	got := SplitMessage("a\n\n  b  \r\nc", 10, 8)
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("lines: %q", got)
	}
	long := strings.Repeat("ab ", 20) // 60 runes
	parts := SplitMessage(long, 25, 8)
	if len(parts) != 3 {
		t.Fatalf("parts: %q", parts)
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 25 || strings.HasSuffix(p, " ") {
			t.Fatalf("bad part %q", p)
		}
	}
	if strings.Join(parts, " ") != strings.TrimSpace(long) {
		t.Fatal("text lost while splitting")
	}
	zh := strings.Repeat("中", 30)
	if p := SplitMessage(zh, 10, 8); len(p) != 3 || p[0] != strings.Repeat("中", 10) {
		t.Fatalf("cjk: %q", p)
	}
	cut := SplitMessage("1\n2\n3\n4", 10, 2)
	if len(cut) != 2 || cut[1] != "2…" {
		t.Fatalf("cap: %q", cut)
	}
}

func TestAgentParse(t *testing.T) {
	a := &agent{}
	if in := a.parse("/who", 500); in.cmd != "/who" {
		t.Fatalf("plain line: %+v", in)
	}
	if in := a.parse(`{"text":"/not a command"}`, 500); len(in.msgs) != 1 || in.msgs[0] != "//not a command" {
		t.Fatalf("text starting with /: %+v", in)
	}
	if in := a.parse(`{"to":"bob","text":"hi\nthere"}`, 500); len(in.msgs) != 2 || in.msgs[1] != "/msg bob there" {
		t.Fatalf("dm: %+v", in)
	}
	if in := a.parse(`{"cmd":"who"}`, 500); in.err == "" {
		t.Fatal("cmd without / accepted")
	}
	if in := a.parse(`{"type":"ping"}`, 500); !in.ping {
		t.Fatal("ping")
	}
	if in := a.parse(`{bad`, 500); in.err == "" {
		t.Fatal("bad json accepted")
	}
}

type agentRW struct {
	io.Reader
	io.Writer
}

// Eight split messages through a tight bucket (burst 3, 20/s): the pacer
// must get every one through without a single "slow down" error.
func TestAgentPacing(t *testing.T) {
	hub := chat.NewHub(chat.Config{MsgBurst: 3, MsgPerSecond: 20})
	human, _ := hub.Connect("ip-h", false)
	bot, _ := hub.Connect("ip-b", false)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() { Agent(agentRW{inR, outW}, hub, bot); close(done) }()
	go io.Copy(io.Discard, outR)

	var lines []string
	for i := range 8 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	b, _ := json.Marshal(map[string]string{"text": strings.Join(lines, "\n")})
	inW.Write(append(b, '\n'))

	got := 0
	deadline := time.After(5 * time.Second)
	for got < 8 {
		select {
		case ev := <-human.Out:
			if ev.Kind == chat.KindChat && ev.From == hub.Nick(bot) {
				got++
			}
		case <-deadline:
			t.Fatalf("only %d of 8 messages arrived", got)
		}
	}
	inW.Close()
	<-done
	// the bot's own stream must not contain a rate-limit error
	for {
		select {
		case ev := <-bot.Out:
			if ev.Kind == chat.KindError {
				t.Fatalf("pacer tripped the limit: %s", ev.En)
			}
		default:
			return
		}
	}
}

// Piped one-shot use: the queued parts must go out before the session ends.
func TestAgentFlushesOnEOF(t *testing.T) {
	hub := chat.NewHub(chat.Config{MsgBurst: 2, MsgPerSecond: 20})
	human, _ := hub.Connect("ip-h", false)
	bot, _ := hub.Connect("ip-b", false)
	in := strings.NewReader(`{"text":"a\nb\nc\nd\ne"}` + "\n")
	Agent(agentRW{in, io.Discard}, hub, bot)
	got := 0
	for {
		select {
		case ev := <-human.Out:
			if ev.Kind == chat.KindChat && ev.From == hub.Nick(bot) {
				got++
			}
			continue
		default:
		}
		break
	}
	if got != 5 {
		t.Fatalf("%d of 5 sent before exit", got)
	}
}
