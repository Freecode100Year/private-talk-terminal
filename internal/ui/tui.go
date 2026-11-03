package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Freecode100Year/private-talk-terminal/internal/chat"
)

const (
	maxEvents    = 1000
	maxInputHist = 50
	sidebarWidth = 24
	minSidebarW  = 76 // terminal width needed to show the sidebar
)

type eventMsg chat.Event
type doneMsg struct{}
type tickMsg time.Time

// TUI is the Bubble Tea model for one SSH session.
type TUI struct {
	hub    *chat.Hub
	client *chat.Client
	fmt    Formatter

	status chat.Status
	events []chat.Event
	width  int
	height int

	vp       viewport.Model
	input    textinput.Model
	hist     []string
	histPos  int
	draft    string
	quitting bool
	reason   string
}

// NewTUI builds the model. width/height come from the PTY request.
func NewTUI(hub *chat.Hub, c *chat.Client, width, height int) *TUI {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "说点什么… type a message or /help"
	in.CharLimit = hub.Config().MaxMsgRunes + 64
	in.Focus()

	t := &TUI{hub: hub, client: c, input: in, vp: viewport.New(), width: width, height: height}
	t.fmt = Formatter{Styled: true, Self: func() string { return t.status.Nick }}
	t.status = hub.Status(c)
	t.layout()
	return t
}

func (t *TUI) waitEvent() tea.Cmd {
	c := t.client
	return func() tea.Msg {
		select {
		case ev := <-c.Out:
			return eventMsg(ev)
		case <-c.Done:
			return doneMsg{}
		}
	}
}

func tick() tea.Cmd {
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (t *TUI) Init() tea.Cmd {
	return tea.Batch(t.waitEvent(), tick(), textinput.Blink)
}

func (t *TUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width, t.height = msg.Width, msg.Height
		t.layout()
		return t, nil

	case eventMsg:
		t.addEvent(chat.Event(msg))
		// drain whatever else is queued so a burst renders once
		for drained := 0; drained < 256; drained++ {
			select {
			case ev := <-t.client.Out:
				t.addEvent(ev)
				continue
			default:
			}
			break
		}
		t.status = t.hub.Status(t.client)
		t.refresh()
		return t, t.waitEvent()

	case doneMsg:
		t.reason = t.client.Reason()
		t.quitting = true
		return t, tea.Quit

	case tickMsg:
		t.status = t.hub.Status(t.client)
		return t, tick()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			t.quitting = true
			return t, tea.Quit
		case "enter":
			return t.submit()
		case "up":
			t.historyMove(-1)
			return t, nil
		case "down":
			t.historyMove(1)
			return t, nil
		case "pgup", "shift+up":
			t.vp.HalfPageUp()
			return t, nil
		case "pgdown", "shift+down":
			t.vp.HalfPageDown()
			return t, nil
		case "home":
			if t.input.Value() == "" {
				t.vp.GotoTop()
				return t, nil
			}
		case "end":
			if t.input.Value() == "" {
				t.vp.GotoBottom()
				return t, nil
			}
		case "esc":
			t.input.Reset()
			t.histPos = len(t.hist)
			t.vp.GotoBottom()
			return t, nil
		case "tab":
			t.complete()
			return t, nil
		case "ctrl+l":
			t.events = nil
			t.refresh()
			return t, nil
		}
	}

	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return t, cmd
}

func (t *TUI) submit() (tea.Model, tea.Cmd) {
	line := t.input.Value()
	t.input.Reset()
	if strings.TrimSpace(line) == "" {
		return t, nil
	}
	if len(t.hist) == 0 || t.hist[len(t.hist)-1] != line {
		t.hist = append(t.hist, line)
		if len(t.hist) > maxInputHist {
			t.hist = t.hist[1:]
		}
	}
	t.histPos = len(t.hist)
	t.draft = ""
	res := t.hub.Handle(t.client, line)
	if res.Clear {
		t.events = nil
		t.refresh()
	}
	if res.Quit {
		t.quitting = true
		return t, tea.Quit
	}
	t.vp.GotoBottom()
	return t, nil
}

func (t *TUI) historyMove(d int) {
	if len(t.hist) == 0 {
		return
	}
	if t.histPos == len(t.hist) {
		t.draft = t.input.Value()
	}
	t.histPos = max(0, min(len(t.hist), t.histPos+d))
	if t.histPos == len(t.hist) {
		t.input.SetValue(t.draft)
	} else {
		t.input.SetValue(t.hist[t.histPos])
	}
	t.input.CursorEnd()
}

// complete does nickname (and command) completion on the last word.
func (t *TUI) complete() {
	v := t.input.Value()
	i := strings.LastIndexByte(v, ' ') + 1
	word := v[i:]
	if word == "" {
		return
	}
	var cands []string
	if i == 0 && strings.HasPrefix(word, "/") {
		for _, c := range []string{"/help", "/rooms", "/join ", "/create", "/leave", "/msg ", "/reply ", "/nick ", "/who", "/me ", "/clear", "/quit"} {
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
		if len([]rune(pick)) <= len([]rune(word)) {
			t.addEvent(chat.Event{Time: time.Now(), Kind: chat.KindInfo, Text: strings.Join(cands, "  ")})
			t.refresh()
			return
		}
	} else if i == 0 && !strings.HasPrefix(pick, "/") {
		pick += ": "
	} else if !strings.HasSuffix(pick, " ") {
		pick += " "
	}
	t.input.SetValue(v[:i] + pick)
	t.input.CursorEnd()
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

func (t *TUI) addEvent(ev chat.Event) {
	t.events = append(t.events, ev)
	if len(t.events) > maxEvents {
		t.events = append(t.events[:0:0], t.events[len(t.events)-maxEvents:]...)
	}
}

// --- layout & rendering --------------------------------------------------------

func (t *TUI) showSidebar() bool { return t.width >= minSidebarW }

func (t *TUI) chatWidth() int {
	w := t.width
	if t.showSidebar() {
		w -= sidebarWidth + 1
	}
	return max(10, w)
}

func (t *TUI) layout() {
	if t.width <= 0 {
		t.width = 80
	}
	if t.height <= 0 {
		t.height = 24
	}
	// header(1) + room line(1) + body + separator(1) + input(1) + footer(1)
	t.vp.SetWidth(t.chatWidth())
	t.vp.SetHeight(max(3, t.height-5))
	t.input.SetWidth(max(10, t.width-4))
	t.refresh()
}

func (t *TUI) refresh() {
	atBottom := t.vp.AtBottom() || t.vp.TotalLineCount() == 0
	w := t.chatWidth()
	var lines []string
	for _, ev := range t.events {
		for _, l := range t.fmt.Lines(ev) {
			wrapped := lipgloss.Wrap(l, w, "")
			lines = append(lines, strings.Split(wrapped, "\n")...)
		}
	}
	t.vp.SetContentLines(lines)
	if atBottom {
		t.vp.GotoBottom()
	}
}

var (
	headerStyle = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15")).Bold(true)
	headerDim   = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("7"))
	roomStyle   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	dim         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sideBorder  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	curRoom     = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	okDot       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

func (t *TUI) header() string {
	s := t.status
	left := headerStyle.Render(" PRIVATE-TALK TERMINAL ")
	right := headerDim.Render(fmt.Sprintf(" %s · Online %d · Rooms %d · Up %s ", s.Nick, s.Online, len(s.Rooms), Uptime(s.Uptime)))
	gap := t.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		right = headerDim.Render(fmt.Sprintf(" %s · %d online ", s.Nick, s.Online))
		gap = max(0, t.width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return truncate(left+headerDim.Render(strings.Repeat(" ", gap))+right, t.width)
}

func (t *TUI) roomLine() string {
	s := t.status
	line := roomStyle.Render(s.RoomLabel) + dim.Render(fmt.Sprintf("  %d here", s.RoomUsers))
	if s.Topic != "" {
		line += dim.Render("  ·  " + s.Topic)
	}
	if s.Muted {
		line += lipgloss.NewStyle().Foreground(cError).Render("  [MUTED]")
	}
	if s.Admin {
		line += lipgloss.NewStyle().Foreground(cNotice).Render("  [ADMIN]")
	}
	return truncate(line, t.width)
}

func (t *TUI) sidebar(h int) string {
	inner := sidebarWidth - 2
	lines := []string{sideBorder.Render("┌─ ROOMS " + strings.Repeat("─", max(0, inner-8)) + "┐")}
	for _, r := range t.status.Rooms {
		label := r.Label
		cnt := fmt.Sprintf("%d", r.Users)
		space := inner - 1 - lipgloss.Width(label) - lipgloss.Width(cnt)
		if space < 1 {
			label = truncate(label, inner-2-lipgloss.Width(cnt))
			space = max(1, inner-1-lipgloss.Width(label)-lipgloss.Width(cnt))
		}
		row := " " + label + strings.Repeat(" ", space) + cnt
		if r.Current {
			row = curRoom.Render(row)
		}
		lines = append(lines, sideBorder.Render("│")+row+sideBorder.Render("│"))
	}
	lines = append(lines, sideBorder.Render("└"+strings.Repeat("─", inner)+"┘"))
	tips := []string{"", " /join <room>", " /create  私人房间", " /msg <nick> <text>", " /nick <name>", " /who   /help"}
	for _, tip := range tips {
		lines = append(lines, dim.Render(truncate(tip, sidebarWidth)))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if pad := sidebarWidth - lipgloss.Width(l); pad > 0 {
			lines[i] = l + strings.Repeat(" ", pad)
		}
	}
	return strings.Join(lines, "\n")
}

func (t *TUI) footer() string {
	keys := "Enter 发送 · ↑↓ 历史 · PgUp/PgDn 滚动 · Tab 补全 · Esc 清空 · Ctrl+C 退出"
	status := okDot.Render("● CONNECTED")
	gap := t.width - lipgloss.Width(keys) - lipgloss.Width(status) - 1
	if gap < 1 {
		keys = "Enter 发送 · ↑↓ · PgUp/PgDn · Ctrl+C"
		gap = max(1, t.width-lipgloss.Width(keys)-lipgloss.Width(status)-1)
	}
	return truncate(dim.Render(keys)+strings.Repeat(" ", gap)+status, t.width)
}

func (t *TUI) View() tea.View {
	if t.quitting {
		msg := "Bye! 再见。\n"
		if t.reason != "" {
			msg = "Disconnected: " + t.reason + "\n"
		}
		return tea.NewView(msg)
	}
	bodyH := t.vp.Height()
	body := t.vp.View()
	if t.showSidebar() {
		sep := sideBorder.Render(strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, t.sidebar(bodyH), sep, body)
	}
	sepLine := sideBorder.Render(strings.Repeat("─", t.width))
	v := tea.NewView(strings.Join([]string{
		t.header(),
		t.roomLine(),
		body,
		sepLine,
		t.input.View(),
		t.footer(),
	}, "\n"))
	v.AltScreen = true
	v.WindowTitle = "Private-Talk Terminal · " + t.status.RoomLabel
	return v
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}
