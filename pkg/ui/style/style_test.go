package style

import (
	"testing"
)

func TestStripAnsiComprehensive(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "standard sgr color",
			input:    "\x1b[31mred text\x1b[0m normal",
			expected: "red text normal",
		},
		{
			name:     "cursor move without m does not eat text",
			input:    "\x1b[2Khello my friend\x1b[1;1Htop",
			expected: "hello my friendtop",
		},
		{
			name:     "cursor visibility codes",
			input:    "\x1b[?25lhidden\x1b[?25hvisible",
			expected: "hiddenvisible",
		},
		{
			name:     "osc sequence with bell",
			input:    "\x1b]0;Title\aContent after title",
			expected: "Content after title",
		},
		{
			name:     "osc sequence with st",
			input:    "\x1b]0;Title\x1b\\Content after ST",
			expected: "Content after ST",
		},
		{
			name:     "24-bit truecolor sequence",
			input:    "\x1b[38;2;255;100;50mTrueColor\x1b[0m Text",
			expected: "TrueColor Text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripAnsi(tt.input)
			if got != tt.expected {
				t.Errorf("StripAnsi(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		expected string
	}{
		{
			name:     "shorter than limit",
			input:    "hello",
			maxRunes: 10,
			expected: "hello",
		},
		{
			name:     "exact limit",
			input:    "hello",
			maxRunes: 5,
			expected: "hello",
		},
		{
			name:     "truncate ascii",
			input:    "hello world from maquis",
			maxRunes: 10,
			expected: "hello w...",
		},
		{
			name:     "truncate multibyte utf8 (emojis)",
			input:    "🌟🚀🎯🔥⚡💡🎉✨",
			maxRunes: 6,
			expected: "🌟🚀🎯...",
		},
		{
			name:     "small limit",
			input:    "abcdef",
			maxRunes: 3,
			expected: "abc",
		},
		{
			name:     "zero limit",
			input:    "abcdef",
			maxRunes: 0,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.input, tt.maxRunes)
			if got != tt.expected {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.input, tt.maxRunes, got, tt.expected)
			}
		})
	}
}
