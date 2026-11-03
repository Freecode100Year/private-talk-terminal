package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

// The full-screen UI is a small purpose-built terminal renderer rather than a
// general TUI framework. A chat screen is mostly "one new line scrolls in at
// the bottom", which a terminal does natively with a scroll region (DECSTBM):
// for each message the server sends just that line. The fixed rows (header,
// rooms, separator, input, footer) are redrawn only when their content
// changes. A busy room therefore costs a few hundred bytes and microseconds
// per session per message, which is what lets a 1 vCPU VPS host hundreds of
// concurrent users.
//
// Layout (H = terminal height):
//
//	1        header bar
//	2        rooms strip
//	3..H-3   chat (scroll region)
//	H-2      separator: current room, topic, scroll state
//	H-1      input line
//	H        key hints + connection status

const (
	maxEvents    = 1000
	maxInputHist = 50
	minWidth     = 20
	minHeight    = 8
)

// WindowSize is a terminal resize notification.
type WindowSize struct{ Width, Height int }

type keyKind int

const (
	kRune keyKind = iota
	kEnter
	kBackspace
	kDelete
	kLeft
	kRight
	kUp
	kDown
	kHome
	kEnd
	kPgUp
	kPgDn
	kTab
	kEsc
	kCtrlC
	kCtrlD
	kCtrlU
	kCtrlW
	kCtrlK
	kCtrlL
	kPaste
)

type key struct {
	kind keyKind
	r    rune
	text string // kPaste
}

// Term runs one full-screen session until the user quits, the hub drops the
// client, or the connection closes.
type Term struct {
	w      io.Writer
	hub    *chat.Hub
	c      *chat.Client
	fmt    Formatter
	status chat.Status
	width  int
	height int

	events  []chat.Event
	evLines []int
	lines   []string // rendered + wrapped for the current width
	scroll  int      // lines scrolled up from the bottom
	unseen  int      // lines that arrived while scrolled up

	input   []rune
	cursor  int
	hist    []string
	histPos int
	draft   string

	last map[int]string // last content drawn on fixed rows, to skip no-op redraws
	out  bytes.Buffer
}

// RunTerm drives a session. in/out are the SSH channel; resize delivers
// window changes.
func RunTerm(rw io.ReadWriter, hub *chat.Hub, c *chat.Client, width, height int, resize <-chan WindowSize) string {
	t := &Term{w: rw, hub: hub, c: c, width: width, height: height, last: map[int]string{}}
	t.fmt = Formatter{Styled: true, Self: func() string { return t.status.Nick }, Loc: func() *time.Location { return t.status.Loc }}
	t.status = hub.Status(c)

	keys := make(chan key, 64)
	stop := make(chan struct{})
	defer close(stop)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readKeys(rw, keys, stop)
	}()

	// alt screen, bracketed paste, clear
	t.out.WriteString("\x1b[?1049h\x1b[?2004h\x1b[H\x1b[2J")
	t.fullRedraw()
	t.flush()
	defer func() {
		t.out.WriteString("\x1b[r\x1b[?2004l\x1b[?1049l")
		t.flush()
	}()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev := <-c.Out:
			t.addEvent(ev)
			for drained := 0; drained < 512; drained++ {
				select {
				case ev := <-c.Out:
					t.addEvent(ev)
					continue
				default:
				}
				break
			}
			t.refreshStatus()
			t.flush()
		case k := <-keys:
			if quit := t.handleKey(k); quit {
				return "Bye! 再见。"
			}
			t.flush()
		case ws := <-resize:
			t.width, t.height = ws.Width, ws.Height
			t.rerender()
			t.fullRedraw()
			t.flush()
		case <-tick.C:
			t.refreshStatus()
			t.flush()
		case <-c.Done:
			if r := c.Reason(); r != "" {
				return "Disconnected: " + r
			}
			return "Bye! 再见。"
		case <-readerDone:
			return ""
		}
	}
}

func (t *Term) flush() {
	if t.out.Len() == 0 {
		return
	}
	_, _ = t.w.Write(t.out.Bytes())
	t.out.Reset()
}

// --- geometry ---------------------------------------------------------------

func (t *Term) tooSmall() bool { return t.width < minWidth || t.height < minHeight }
func (t *Term) chatTop() int   { return 3 }
func (t *Term) chatBot() int   { return t.height - 3 }
func (t *Term) bodyH() int     { return max(1, t.chatBot()-t.chatTop()+1) }
func (t *Term) wrapW() int     { return max(10, t.width-1) } // never touch the last column

func (t *Term) moveTo(row, col int) { fmt.Fprintf(&t.out, "\x1b[%d;%dH", row, col) }

// --- events & scrollback ------------------------------------------------------

func (t *Term) render(ev chat.Event) []string {
	w := t.wrapW()
	var out []string
	for _, l := range t.fmt.Lines(ev) {
		if ansi.StringWidth(l) <= w {
			out = append(out, l)
			continue
		}
		// continuation lines are indented under the message text
		const indent = "        "
		for i, wl := range strings.Split(lipgloss.Wrap(l, w-len(indent), ""), "\n") {
			if i > 0 {
				wl = indent + wl
			}
			out = append(out, ansi.Truncate(wl, w, ""))
		}
	}
	return out
}

func (t *Term) addEvent(ev chat.Event) {
	t.events = append(t.events, ev)
	r := t.render(ev)
	t.lines = append(t.lines, r...)
	t.evLines = append(t.evLines, len(r))
	if len(t.events) > maxEvents+maxEvents/4 {
		drop := len(t.events) - maxEvents
		n := 0
		for _, k := range t.evLines[:drop] {
			n += k
		}
		t.events = append(t.events[:0:0], t.events[drop:]...)
		t.evLines = append(t.evLines[:0:0], t.evLines[drop:]...)
		t.lines = append(t.lines[:0:0], t.lines[n:]...)
		t.scroll = min(t.scroll, max(0, len(t.lines)-t.bodyH()))
	}
	if t.tooSmall() {
		return
	}
	if t.scroll > 0 { // reader is looking at history: don't move the view
		t.scroll += len(r)
		t.unseen += len(r)
		t.drawSeparator()
		return
	}
	// Scroll the chat region: put the cursor on its last row and emit a
	// newline per line; the terminal shifts the region up by itself.
	t.moveTo(t.chatBot(), 1)
	for _, l := range r {
		t.out.WriteString("\n\r")
		t.out.WriteString(l)
		t.out.WriteString("\x1b[0m\x1b[K")
	}
	t.placeCursor()
}

func (t *Term) rerender() {
	t.lines, t.evLines = t.lines[:0], t.evLines[:0]
	for _, ev := range t.events {
		r := t.render(ev)
		t.lines = append(t.lines, r...)
		t.evLines = append(t.evLines, len(r))
	}
	t.scroll, t.unseen = 0, 0
}

func (t *Term) clearChat() {
	t.events, t.evLines, t.lines, t.scroll, t.unseen = nil, nil, nil, 0, 0
	t.drawChat()
	t.drawSeparator()
	t.placeCursor()
}

func (t *Term) scrollBy(n int) {
	maxScroll := max(0, len(t.lines)-t.bodyH())
	s := max(0, min(t.scroll+n, maxScroll))
	if s == t.scroll {
		return
	}
	t.scroll = s
	if s == 0 {
		t.unseen = 0
	}
	t.drawChat()
	t.drawSeparator()
	t.placeCursor()
}

// --- drawing -----------------------------------------------------------------

func (t *Term) fullRedraw() {
	t.last = map[int]string{}
	t.out.WriteString("\x1b[r\x1b[0m\x1b[H\x1b[2J")
	if t.tooSmall() {
		t.out.WriteString("Window too small · 窗口太小")
		return
	}
	fmt.Fprintf(&t.out, "\x1b]2;%s\x07", "Private-Talk Terminal")
	fmt.Fprintf(&t.out, "\x1b[%d;%dr", t.chatTop(), t.chatBot())
	t.drawFixed(1, t.headerLine())
	t.drawFixed(2, t.roomsLine())
	t.drawChat()
	t.drawSeparator()
	t.drawFixed(t.height, t.footerLine())
	t.drawInput()
}

func (t *Term) drawChat() {
	if t.tooSmall() {
		return
	}
	h := t.bodyH()
	end := len(t.lines) - t.scroll
	start := max(0, end-h)
	vis := t.lines[start:end]
	pad := h - len(vis)
	for i := 0; i < h; i++ {
		t.moveTo(t.chatTop()+i, 1)
		if i >= pad {
			t.out.WriteString(vis[i-pad])
			t.out.WriteString("\x1b[0m")
		}
		t.out.WriteString("\x1b[K")
	}
}

// drawFixed redraws a fixed row if its content changed.
func (t *Term) drawFixed(row int, content string) {
	if t.tooSmall() || t.last[row] == content {
		return
	}
	t.last[row] = content
	t.out.WriteString("\x1b7") // save cursor
	t.moveTo(row, 1)
	t.out.WriteString(content)
	t.out.WriteString("\x1b[0m\x1b[K\x1b8")
}

func (t *Term) refreshStatus() {
	t.status = t.hub.Status(t.c)
	t.drawFixed(1, t.headerLine())
	t.drawFixed(2, t.roomsLine())
	t.drawSeparator()
}

var (
	headerStyle = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15")).Bold(true)
	headerDim   = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("7"))
	curRoom     = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	dim         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	promptStyle = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	okDot       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle   = lipgloss.NewStyle().Foreground(cNotice)
	errStyle    = lipgloss.NewStyle().Foreground(cError)
)

func (t *Term) headerLine() string {
	s := t.status
	loc := s.Loc
	if loc == nil {
		loc = time.Local
	}
	left := " PRIVATE-TALK TERMINAL "
	right := fmt.Sprintf(" %s · Online %d · Up %s · %s ", s.Nick, s.Online, Uptime(s.Uptime), time.Now().In(loc).Format("15:04 MST"))
	if ansi.StringWidth(left)+ansi.StringWidth(right) > t.width {
		right = fmt.Sprintf(" %s · %d online ", s.Nick, s.Online)
	}
	gap := max(0, t.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	return ansi.Truncate(headerStyle.Render(left)+headerDim.Render(strings.Repeat(" ", gap)+right), t.width, "")
}

func (t *Term) roomsLine() string {
	var b strings.Builder
	b.WriteString(dim.Render("ROOMS "))
	for _, r := range t.status.Rooms {
		item := fmt.Sprintf("%s %d", r.Label, r.Users)
		if r.Current {
			b.WriteString(curRoom.Render("▸" + item))
		} else {
			b.WriteString(" " + item)
		}
		b.WriteString("  ")
	}
	return ansi.Truncate(b.String(), t.width, "…")
}

func (t *Term) drawSeparator() {
	s := t.status
	txt := "── " + s.RoomLabel + fmt.Sprintf(" · %d here", s.RoomUsers)
	if s.Topic != "" {
		txt += " · " + s.Topic
	}
	txt += " "
	line := dim.Render(txt)
	if s.Muted {
		line += errStyle.Render("[MUTED] ")
	}
	if s.Admin {
		line += warnStyle.Render("[ADMIN] ")
	}
	if t.scroll > 0 {
		more := fmt.Sprintf("↑ scrolled %d", t.scroll)
		if t.unseen > 0 {
			more = fmt.Sprintf("↓ %d new", t.unseen)
		}
		line += warnStyle.Render(more + " · End 回到底部 ")
	}
	if pad := t.width - ansi.StringWidth(line); pad > 0 {
		line += dim.Render(strings.Repeat("─", pad))
	}
	t.drawFixed(t.height-2, ansi.Truncate(line, t.width, ""))
}

func (t *Term) footerLine() string {
	keys := "Enter 发送 · ↑↓ 历史 · PgUp/PgDn 滚动 · Tab 补全 · Esc 清空 · Ctrl+C 退出"
	st := okDot.Render("● CONNECTED")
	gap := t.width - ansi.StringWidth(keys) - ansi.StringWidth(st) - 1
	if gap < 1 {
		keys = "Enter 发送 · PgUp/PgDn · Ctrl+C 退出"
		gap = max(1, t.width-ansi.StringWidth(keys)-ansi.StringWidth(st)-1)
	}
	return ansi.Truncate(dim.Render(keys)+strings.Repeat(" ", gap)+st, t.width, "")
}

// inputView returns the visible part of the input and the cursor column.
func (t *Term) inputView() (string, int) {
	prompt := "> "
	avail := max(4, t.width-ansi.StringWidth(prompt)-1)
	if len(t.input) == 0 {
		ph := ansi.Truncate("说点什么… type a message or /help", avail, "")
		return promptStyle.Render(prompt) + dim.Render(ph), ansi.StringWidth(prompt) + 1
	}
	// choose a start so the cursor stays visible
	start := 0
	for ansi.StringWidth(string(t.input[start:t.cursor])) > avail-1 {
		start++
	}
	var b strings.Builder
	w := 0
	for _, r := range t.input[start:] {
		rw := max(1, ansi.StringWidth(string(r)))
		if w+rw > avail {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	col := ansi.StringWidth(prompt) + ansi.StringWidth(string(t.input[start:t.cursor])) + 1
	return promptStyle.Render(prompt) + b.String(), col
}

func (t *Term) drawInput() {
	if t.tooSmall() {
		return
	}
	s, _ := t.inputView()
	t.moveTo(t.height-1, 1)
	t.out.WriteString(s)
	t.out.WriteString("\x1b[0m\x1b[K")
	t.placeCursor()
}

func (t *Term) placeCursor() {
	if t.tooSmall() {
		return
	}
	_, col := t.inputView()
	t.moveTo(t.height-1, min(col, t.width))
}

// --- input --------------------------------------------------------------------

func (t *Term) handleKey(k key) (quit bool) {
	switch k.kind {
	case kCtrlC:
		return true
	case kCtrlD:
		if len(t.input) == 0 {
			return true
		}
		t.deleteAt(t.cursor)
	case kEnter:
		return t.submit()
	case kRune:
		t.insert(string(k.r))
	case kPaste:
		// newlines in a paste become spaces: pasting never sends by accident
		t.insert(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(k.text))
	case kBackspace:
		if t.cursor > 0 {
			t.cursor--
			t.deleteAt(t.cursor)
		}
	case kDelete:
		t.deleteAt(t.cursor)
	case kLeft:
		t.cursor = max(0, t.cursor-1)
	case kRight:
		t.cursor = min(len(t.input), t.cursor+1)
	case kHome:
		if len(t.input) == 0 {
			t.scrollBy(len(t.lines))
			return false
		}
		t.cursor = 0
	case kEnd:
		if len(t.input) == 0 {
			t.scrollBy(-t.scroll)
			return false
		}
		t.cursor = len(t.input)
	case kUp:
		t.historyMove(-1)
	case kDown:
		t.historyMove(1)
	case kPgUp:
		t.scrollBy(max(1, t.bodyH()-2))
		return false
	case kPgDn:
		t.scrollBy(-max(1, t.bodyH()-2))
		return false
	case kEsc:
		t.input, t.cursor = nil, 0
		t.histPos = len(t.hist)
		t.scrollBy(-t.scroll)
	case kCtrlU:
		t.input, t.cursor = append([]rune(nil), t.input[t.cursor:]...), 0
	case kCtrlK:
		t.input = t.input[:t.cursor]
	case kCtrlW:
		i := t.cursor
		for i > 0 && t.input[i-1] == ' ' {
			i--
		}
		for i > 0 && t.input[i-1] != ' ' {
			i--
		}
		t.input = append(t.input[:i], t.input[t.cursor:]...)
		t.cursor = i
	case kCtrlL:
		t.fullRedraw()
		return false
	case kTab:
		t.complete()
	}
	t.drawInput()
	return false
}

func (t *Term) insert(s string) {
	limit := t.hub.Config().MaxMsgRunes + 64
	var rs []rune
	for _, r := range s {
		if chat.Printable(r) {
			rs = append(rs, r)
		}
	}
	if room := limit - len(t.input); len(rs) > room {
		rs = rs[:max(0, room)]
	}
	t.input = append(t.input[:t.cursor], append(rs, t.input[t.cursor:]...)...)
	t.cursor += len(rs)
}

func (t *Term) deleteAt(i int) {
	if i >= 0 && i < len(t.input) {
		t.input = append(t.input[:i], t.input[i+1:]...)
	}
}

func (t *Term) submit() bool {
	line := string(t.input)
	t.input, t.cursor = nil, 0
	if strings.TrimSpace(line) == "" {
		t.drawInput()
		return false
	}
	if len(t.hist) == 0 || t.hist[len(t.hist)-1] != line {
		t.hist = append(t.hist, line)
		if len(t.hist) > maxInputHist {
			t.hist = t.hist[1:]
		}
	}
	t.histPos, t.draft = len(t.hist), ""
	if t.scroll > 0 {
		t.scrollBy(-t.scroll)
	}
	res := t.hub.Handle(t.c, line)
	if res.Quit {
		return true
	}
	if res.Clear {
		t.clearChat()
	}
	t.refreshStatus()
	t.drawInput()
	return false
}

func (t *Term) historyMove(d int) {
	if len(t.hist) == 0 {
		return
	}
	if t.histPos == len(t.hist) {
		t.draft = string(t.input)
	}
	t.histPos = max(0, min(len(t.hist), t.histPos+d))
	v := t.draft
	if t.histPos < len(t.hist) {
		v = t.hist[t.histPos]
	}
	t.input = []rune(v)
	t.cursor = len(t.input)
}

var completions = []string{"/help", "/rooms", "/join ", "/create", "/leave", "/msg ", "/reply ", "/nick ", "/who", "/me ", "/ignore ", "/unignore ", "/report ", "/tz ", "/clear", "/quit"}

// complete does command and nickname completion on the word before the cursor.
func (t *Term) complete() {
	before := string(t.input[:t.cursor])
	i := strings.LastIndexByte(before, ' ') + 1
	word := before[i:]
	if word == "" {
		return
	}
	var cands []string
	if i == 0 && strings.HasPrefix(word, "/") {
		for _, c := range completions {
			if strings.HasPrefix(c, strings.ToLower(word)) {
				cands = append(cands, c)
			}
		}
	} else {
		w := strings.ToLower(strings.TrimPrefix(word, "@"))
		for _, n := range t.hub.Nicks() {
			if strings.HasPrefix(strings.ToLower(n), w) {
				cands = append(cands, n)
			}
		}
	}
	if len(cands) == 0 {
		return
	}
	sort.Strings(cands)
	pick := cands[0]
	if len(cands) > 1 {
		pick = commonPrefix(cands)
		if utf8.RuneCountInString(pick) <= utf8.RuneCountInString(word) {
			t.addEvent(chat.Event{Time: time.Now(), Kind: chat.KindInfo, Text: strings.Join(cands, "  ")})
			return
		}
	} else if i == 0 && !strings.HasPrefix(pick, "/") {
		pick += ": "
	} else if !strings.HasSuffix(pick, " ") {
		pick += " "
	}
	rest := t.input[t.cursor:]
	t.input = append([]rune(before[:i]+pick), rest...)
	t.cursor = utf8.RuneCountInString(before[:i] + pick)
}

func commonPrefix(s []string) string {
	p := []rune(s[0])
	for _, x := range s[1:] {
		r := []rune(x)
		n := 0
		for n < len(p) && n < len(r) && strings.EqualFold(string(p[n]), string(r[n])) {
			n++
		}
		p = p[:n]
	}
	return string(p)
}

// --- key decoding ---------------------------------------------------------------

// readKeys decodes the byte stream from an xterm-compatible terminal
// (including Windows Terminal / conhost via OpenSSH) into keys.
func readKeys(r io.Reader, out chan<- key, stop <-chan struct{}) {
	br := bufio.NewReaderSize(r, 4096)
	send := func(k key) bool {
		select {
		case out <- k:
			return true
		case <-stop:
			return false
		}
	}
	lastCR := false
	for {
		b, err := br.ReadByte()
		if err != nil {
			return
		}
		var k key
		ok := true
		switch {
		case b == '\r':
			k = key{kind: kEnter}
		case b == '\n':
			if lastCR { // CRLF from some clients: one Enter
				lastCR = false
				continue
			}
			k = key{kind: kEnter}
		case b == 0x7f || b == 0x08:
			k = key{kind: kBackspace}
		case b == 0x09:
			k = key{kind: kTab}
		case b == 0x03:
			k = key{kind: kCtrlC}
		case b == 0x04:
			k = key{kind: kCtrlD}
		case b == 0x01:
			k = key{kind: kHome}
		case b == 0x05:
			k = key{kind: kEnd}
		case b == 0x02:
			k = key{kind: kLeft}
		case b == 0x06:
			k = key{kind: kRight}
		case b == 0x10:
			k = key{kind: kUp}
		case b == 0x0e:
			k = key{kind: kDown}
		case b == 0x15:
			k = key{kind: kCtrlU}
		case b == 0x17:
			k = key{kind: kCtrlW}
		case b == 0x0b:
			k = key{kind: kCtrlK}
		case b == 0x0c:
			k = key{kind: kCtrlL}
		case b == 0x1b:
			k, ok = readEscape(br)
		case b < 0x20:
			ok = false
		default:
			_ = br.UnreadByte()
			ru, _, err := br.ReadRune()
			if err != nil {
				return
			}
			if ru == utf8.RuneError {
				ok = false
			}
			k = key{kind: kRune, r: ru}
		}
		lastCR = b == '\r'
		if ok && !send(k) {
			return
		}
	}
}

func readEscape(br *bufio.Reader) (key, bool) {
	if br.Buffered() == 0 { // a lone ESC arrives in a packet by itself
		return key{kind: kEsc}, true
	}
	b, err := br.ReadByte()
	if err != nil {
		return key{kind: kEsc}, true
	}
	switch b {
	case '[':
		var params []byte
		for i := 0; i < 32; i++ {
			c, err := br.ReadByte()
			if err != nil {
				return key{}, false
			}
			if c >= 0x40 && c <= 0x7e {
				return csiKey(br, string(params), c)
			}
			params = append(params, c)
		}
		return key{}, false
	case 'O':
		c, err := br.ReadByte()
		if err != nil {
			return key{}, false
		}
		return csiKey(br, "", c)
	case 0x1b:
		_ = br.UnreadByte()
		return key{kind: kEsc}, true
	}
	return key{}, false // Alt+key: ignored
}

func csiKey(br *bufio.Reader, params string, final byte) (key, bool) {
	switch final {
	case 'A':
		return key{kind: kUp}, true
	case 'B':
		return key{kind: kDown}, true
	case 'C':
		return key{kind: kRight}, true
	case 'D':
		return key{kind: kLeft}, true
	case 'H':
		return key{kind: kHome}, true
	case 'F':
		return key{kind: kEnd}, true
	case 'Z': // shift+tab
		return key{kind: kTab}, true
	case '~':
		p, _, _ := strings.Cut(params, ";")
		switch p {
		case "1", "7":
			return key{kind: kHome}, true
		case "4", "8":
			return key{kind: kEnd}, true
		case "3":
			return key{kind: kDelete}, true
		case "5":
			return key{kind: kPgUp}, true
		case "6":
			return key{kind: kPgDn}, true
		case "200":
			return readPaste(br), true
		}
	}
	return key{}, false
}

// readPaste collects text until the bracketed-paste end marker (capped).
func readPaste(br *bufio.Reader) key {
	const end = "\x1b[201~"
	var buf []byte
	for len(buf) < 16*1024 {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		buf = append(buf, b)
		if bytes.HasSuffix(buf, []byte(end)) {
			buf = buf[:len(buf)-len(end)]
			break
		}
	}
	return key{kind: kPaste, text: string(buf)}
}
