package camoufoxnative

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestPromptHeadUTF16Boundary(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"empty", "", ""},
		{"short emoji", "hello 🎁", "hello 🎁"},
		{"ASCII boundary", strings.Repeat("a", 25001), strings.Repeat("a", 25000)},
		{"Chinese boundary", strings.Repeat("中", 25001), strings.Repeat("中", 25000)},
		{"emoji inside prefix", "🎁" + strings.Repeat("a", 25000), "🎁" + strings.Repeat("a", 24998)},
		{"emoji fits exactly", strings.Repeat("a", 24998) + "🎁tail", strings.Repeat("a", 24998) + "🎁"},
		{"emoji crosses boundary", strings.Repeat("a", 24999) + "🎁tail", strings.Repeat("a", 24999)},
		{"all emoji", strings.Repeat("🎁", 12501), strings.Repeat("🎁", 12500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := promptHead(tc.input)
			if got != tc.want {
				t.Errorf("incorrect prefix: got %d UTF-16 units, want %d", len(utf16.Encode([]rune(got))), len(utf16.Encode([]rune(tc.want))))
			}
			if !utf8.ValidString(got) || !strings.HasPrefix(tc.input, got) {
				t.Fatal("prefix must preserve complete Unicode characters")
			}
			if n := len(utf16.Encode([]rune(got))); n > promptFillLimit {
				t.Errorf("browser string length %d exceeds %d", n, promptFillLimit)
			}
		})
	}
}
