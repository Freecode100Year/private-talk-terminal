package chat

import (
	"strconv"
	"strings"
)

// Text sizes for /font, in CSS pixels. The web page applies them directly;
// in an SSH session the terminal owns the font, so /font explains how to
// zoom it there instead.
const (
	FontMin     = 12
	FontMax     = 40
	FontDefault = 16
	fontStep    = 2
)

// ParseFont applies a /font argument to the current size: "+" / "-" (also
// "bigger", "大", …) step it, a number sets it, "reset" restores the default.
// The result is clamped to FontMin..FontMax.
func ParseFont(cur int, arg string) (int, bool) {
	if cur == 0 {
		cur = FontDefault
	}
	a := strings.ToLower(strings.TrimSpace(arg))
	a = strings.TrimSuffix(a, "px")
	n := cur
	switch a {
	case "+", "++", "up", "big", "bigger", "larger", "in", "大", "大一点", "放大", "调大":
		n = cur + fontStep
	case "-", "--", "down", "small", "smaller", "out", "小", "小一点", "缩小", "调小":
		n = cur - fontStep
	case "reset", "default", "normal", "0", "默认", "恢复", "重置":
		n = FontDefault
	default:
		v, err := strconv.Atoi(a)
		if err != nil {
			return cur, false
		}
		n = v
	}
	return min(FontMax, max(FontMin, n)), true
}

func (h *Hub) fontLocked(c *Client, arg string) {
	if !c.web {
		// The server can't resize an SSH client's font: say how to do it.
		c.send(&Event{Kind: KindInfo,
			Text: "终端的字号由你的 SSH 软件决定，服务器改不了：",
			En:   "Your SSH app owns the font size; the server can't change it:",
			Lines: []string{
				"Windows Terminal / Linux：Ctrl + =  放大 bigger · Ctrl + -  缩小 smaller · Ctrl + 0  还原 reset",
				"macOS 终端 Terminal / iTerm2：⌘ + =  放大 bigger · ⌘ + -  缩小 smaller · ⌘ + 0  还原 reset",
				"手机 Phones（Termius、iSH）：双指缩放 · pinch to zoom",
				"网页版可以直接用 /font + 和 /font - · on the web page, /font + and /font - work directly",
			}})
		return
	}
	cur := c.font
	if cur == 0 {
		cur = FontDefault
	}
	if strings.TrimSpace(arg) == "" {
		h.infof(c, "字号 %dpx。/font + 调大、/font - 调小、/font 20 指定、/font reset 默认（%dpx）。",
			"Text size %dpx. /font + bigger, /font - smaller, /font 20 to set, /font reset for the default (%dpx).", cur, FontDefault)
		return
	}
	n, ok := ParseFont(cur, arg)
	if !ok {
		h.errorf(c, "用法：/font +、/font -、/font 20（%d–%d）或 /font reset", "Usage: /font +, /font -, /font 20 (%d–%d) or /font reset", FontMin, FontMax)
		return
	}
	c.font = n
	h.infof(c, "字号 %dpx。", "Text size %dpx.", n)
}

// SetFont changes the client's text size without a message (the web page's
// A− / A+ buttons). Unknown arguments are ignored.
func (h *Hub) SetFont(c *Client, arg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n, ok := ParseFont(c.font, arg); ok {
		c.font = n
	}
}

// SetWeb marks a client as connected through the web page.
func (h *Hub) SetWeb(c *Client) {
	h.mu.Lock()
	c.web = true
	h.mu.Unlock()
}
