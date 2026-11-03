package ui

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

// Plain runs a line-mode session: no colours, no cursor movement, no
// alternate screen. Used for `ssh plain@host`, for `ssh -T` (no PTY) and
// for TERM=dumb. When hasPty is true the server echoes keystrokes and
// redraws the input line itself (the client terminal is in raw mode);
// otherwise the client's own terminal handles line editing.
func Plain(rw io.ReadWriter, hub *chat.Hub, c *chat.Client, hasPty bool, width int) {
	p := &plain{w: rw, hub: hub, c: c, pty: hasPty, width: width}
	p.fmt = Formatter{Styled: false, Self: func() string { return hub.Nick(c) }, Loc: func() *time.Location { return hub.Loc(c) }}
	p.nl = "\n"
	if hasPty {
		p.nl = "\r\n"
	}
	if p.width <= 0 {
		p.width = 80
	}
	p.write("PRIVATE-TALK TERMINAL (plain mode) · /help · /quit" + p.nl)

	lines := make(chan string)
	quit := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go p.readInput(rw, lines, quit, stop)
	p.drawPrompt()

	for {
		select {
		case ev := <-c.Out:
			p.printEvent(ev)
		case <-c.Done:
			if r := c.Reason(); r != "" {
				p.printRaw("Disconnected: " + r)
			}
			return
		case <-quit:
			return
		case line := <-lines:
			res := hub.Handle(c, line)
			if res.Quit {
				p.printRaw("Bye! 再见。")
				return
			}
			p.drawPrompt()
		}
	}
}

type plain struct {
	mu    sync.Mutex
	w     io.Writer
	hub   *chat.Hub
	c     *chat.Client
	fmt   Formatter
	pty   bool
	nl    string
	width int
	buf   []rune // current input (pty mode only)
}

func (p *plain) write(s string) {
	_, _ = io.WriteString(p.w, s)
}

// clearInput erases the in-progress input line without ANSI sequences.
func (p *plain) clearInputLocked() {
	if !p.pty {
		return
	}
	n := ansi.StringWidth("> "+string(p.buf)) % max(1, p.width)
	p.write("\r" + strings.Repeat(" ", n) + "\r")
}

func (p *plain) drawPromptLocked() {
	if p.pty {
		p.write("> " + string(p.buf))
	}
}

func (p *plain) drawPrompt() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drawPromptLocked()
}

func (p *plain) printRaw(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearInputLocked()
	p.write(s + p.nl)
}

func (p *plain) printEvent(ev chat.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearInputLocked()
	for _, l := range p.fmt.Lines(ev) {
		p.write(l + p.nl)
	}
	p.drawPromptLocked()
}

func (p *plain) readInput(r io.Reader, lines chan<- string, quit chan<- struct{}, stop <-chan struct{}) {
	defer close(quit)
	send := func(l string) bool {
		select {
		case lines <- l:
			return true
		case <-stop:
			return false
		}
	}
	if !p.pty {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 4096), 64*1024)
		for sc.Scan() {
			if !send(strings.TrimRight(sc.Text(), "\r")) {
				return
			}
		}
		return
	}
	br := bufio.NewReader(r)
	for {
		ru, _, err := br.ReadRune()
		if err != nil {
			return
		}
		switch {
		case ru == '\r' || ru == '\n':
			p.mu.Lock()
			line := string(p.buf)
			p.buf = p.buf[:0]
			p.write(p.nl)
			p.mu.Unlock()
			if line != "" {
				if !send(line) {
					return
				}
			} else {
				p.drawPrompt()
			}
		case ru == 0x03 || ru == 0x04: // Ctrl+C, Ctrl+D
			return
		case ru == 0x7f || ru == 0x08: // Backspace
			p.mu.Lock()
			if n := len(p.buf); n > 0 {
				w := max(1, ansi.StringWidth(string(p.buf[n-1])))
				p.buf = p.buf[:n-1]
				p.write(strings.Repeat("\b", w) + strings.Repeat(" ", w) + strings.Repeat("\b", w))
			}
			p.mu.Unlock()
		case ru == 0x15: // Ctrl+U: kill line
			p.mu.Lock()
			p.clearInputLocked()
			p.buf = p.buf[:0]
			p.drawPromptLocked()
			p.mu.Unlock()
		case ru == 0x1b: // escape sequence (arrow keys etc.): swallow it
			skipEscape(br)
		case ru < 0x20 || ru == utf8.RuneError:
			// ignore other control keys
		default:
			p.mu.Lock()
			if len(p.buf) < p.hub.Config().MaxMsgRunes+64 {
				p.buf = append(p.buf, ru)
				p.write(string(ru))
			}
			p.mu.Unlock()
		}
	}
}

// skipEscape consumes the rest of a CSI/SS3 sequence after ESC.
func skipEscape(br *bufio.Reader) {
	b, err := br.ReadByte()
	if err != nil {
		return
	}
	if b != '[' && b != 'O' {
		return
	}
	for i := 0; i < 16; i++ {
		b, err = br.ReadByte()
		if err != nil || (b >= 0x40 && b <= 0x7e) {
			return
		}
	}
}
