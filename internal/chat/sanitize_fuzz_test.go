package chat

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSanitizeInvisible(t *testing.T) {
	cases := map[string]string{
		"a​b":                    "ab",       // zero-width space
		"a⁠b­c":                  "abc",      // word joiner, soft hyphen
		"hi\U000E0041\U000E0042": "hi",       // hidden tag text
		"👨‍👩‍👧":                  "👨‍👩‍👧",    // family emoji keeps ZWJ
		"می‌خواهم":               "می‌خواهم", // Persian keeps ZWNJ
		"\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F": "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", // England flag
		"\U0001F3F4\U000E007Fx\U000E0041":                                        "\U0001F3F4\U000E007Fx",                                                  // tags after the flag ended are dropped
	}
	for in, want := range cases {
		if got := Sanitize(in, 0); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzSanitize: whatever goes in, nothing that can drive a terminal or hide
// text comes out, and the length cap holds.
//
//	go test -fuzz=FuzzSanitize ./internal/chat
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{
		"hello", "\x1b]52;c;ZXZpbA==\x07", "\x1b[6n", "\x9b31m", "\x1bP+q\x1b\\",
		"‮evil", "a​b", "\U000E0041", "\xff\xfe", "\x1b(0lqk", "中文\t测试",
	} {
		f.Add(s, 20)
	}
	f.Fuzz(func(t *testing.T, in string, max int) {
		max = max % 1000
		out := Sanitize(in, max)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out of %q", in)
		}
		if max > 0 && utf8.RuneCountInString(out) > max {
			t.Fatalf("cap %d exceeded: %d runes", max, utf8.RuneCountInString(out))
		}
		if out != strings.TrimSpace(out) {
			t.Fatalf("untrimmed output %q", out)
		}
		prevFlag := false
		for _, r := range out {
			switch {
			case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
				t.Fatalf("control %U in output of %q", r, in)
			case isBidiControl(r):
				t.Fatalf("bidi control %U in output of %q", r, in)
			case isTag(r):
				if !prevFlag {
					t.Fatalf("stray tag %U in output of %q", r, in)
				}
			case unicode.Is(unicode.Cf, r) && r != 0x200c && r != 0x200d:
				t.Fatalf("invisible %U in output of %q", r, in)
			}
			prevFlag = r == 0x1f3f4 || (isTag(r) && r != 0xe007f)
		}
	})
}
