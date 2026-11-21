package irc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFormatting(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Span
	}{
		{"plain", "hello", []Span{{Text: "hello"}}},
		{"empty", "", nil},
		{"only codes", "\x02\x0304\x0f", nil},
		{"bold", "a\x02b\x02c", []Span{{Text: "a"}, {Bold: true, Text: "b"}, {Text: "c"}}},
		{"italic", "\x1da", []Span{{Italic: true, Text: "a"}}},
		{"underline", "\x1fa", []Span{{Underline: true, Text: "a"}}},
		{"strikethrough", "\x1ea", []Span{{Strikethrough: true, Text: "a"}}},
		{"monospace", "\x11a", []Span{{Monospace: true, Text: "a"}}},
		{"reverse", "\x16a", []Span{{Reverse: true, Text: "a"}}},
		{
			"toggles combine",
			"a\x02b\x1dc\x02d\x1d\x1fe\x1ef\x11g\x16h",
			[]Span{
				{Text: "a"},
				{Bold: true, Text: "b"},
				{Bold: true, Italic: true, Text: "c"},
				{Italic: true, Text: "d"},
				{Underline: true, Text: "e"},
				{Underline: true, Strikethrough: true, Text: "f"},
				{Underline: true, Strikethrough: true, Monospace: true, Text: "g"},
				{Underline: true, Strikethrough: true, Monospace: true, Reverse: true, Text: "h"},
			},
		},
		{
			"reset clears everything",
			"\x02\x1d\x0304,02a\x0fb",
			[]Span{
				{Bold: true, Italic: true, Foreground: "#ff0000", Background: "#00007f", Text: "a"},
				{Text: "b"},
			},
		},
		{
			"same style is merged",
			"a\x02\x02b\x0304c\x0304d",
			[]Span{
				{Text: "ab"},
				{Foreground: "#ff0000", Text: "cd"},
			},
		},
		{"single digit colour", "\x034a", []Span{{Foreground: "#ff0000", Text: "a"}}},
		{
			"two digits are always read",
			"\x03044\x0312345",
			[]Span{
				{Foreground: "#ff0000", Text: "4"},
				{Foreground: "#0000fc", Text: "345"},
			},
		},
		{
			"foreground keeps background",
			"\x031,8a\x034b",
			[]Span{
				{Foreground: "#000000", Background: "#ffff00", Text: "a"},
				{Foreground: "#ff0000", Background: "#ffff00", Text: "b"},
			},
		},
		{
			"bare colour code resets colours only",
			"\x02\x034,8a\x03b",
			[]Span{
				{Bold: true, Foreground: "#ff0000", Background: "#ffff00", Text: "a"},
				{Bold: true, Text: "b"},
			},
		},
		{"extended colour", "\x0352a", []Span{{Foreground: "#ff0000", Text: "a"}}},
		{"default colour", "\x0304,99a", []Span{{Foreground: "#ff0000", Text: "a"}}},
		{
			"hex",
			"\x04FF8000,000000a\x04ff0000b",
			[]Span{
				{Foreground: "#ff8000", Background: "#000000", Text: "a"},
				{Foreground: "#ff0000", Background: "#000000", Text: "b"},
			},
		},
		{
			"bare hex code resets colours",
			"\x04ff0000a\x04b",
			[]Span{{Foreground: "#ff0000", Text: "a"}, {Text: "b"}},
		},
		{"multi-byte text", "\x02héllo\x02 wörld", []Span{{Bold: true, Text: "héllo"}, {Text: " wörld"}}},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, ParseFormatting(tt.text))
			},
		)
	}
}

func TestParseFormatting_malformedColours(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Span
	}{
		{"colour at end", "a\x03", []Span{{Text: "a"}}},
		{"hex at end", "a\x04", []Span{{Text: "a"}}},
		{"comma without foreground is text", "\x03,5a", []Span{{Text: ",5a"}}},
		{"comma without background is text", "\x034,a", []Span{{Foreground: "#ff0000", Text: ",a"}}},
		{"trailing comma is text", "\x034,", []Span{{Foreground: "#ff0000", Text: ","}}},
		{"third digit is text", "\x03123a", []Span{{Foreground: "#0000fc", Text: "3a"}}},
		{
			"third background digit is text",
			"\x031,123a",
			[]Span{{Foreground: "#000000", Background: "#0000fc", Text: "3a"}},
		},
		{"short hex is text", "\x04abcb", []Span{{Text: "abcb"}}},
		{"non-hex digits are text", "\x04ff00zz", []Span{{Text: "ff00zz"}}},
		{"short hex background is text", "\x04ff0000,abc", []Span{{Foreground: "#ff0000", Text: ",abc"}}},
		{"numbered background on hex is text", "\x04ff0000,12", []Span{{Foreground: "#ff0000", Text: ",12"}}},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, ParseFormatting(tt.text))
			},
		)
	}
}

func TestStripFormatting(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"plain", "plain", "plain"},
		{"empty", "", ""},
		{"toggles", "\x02b\x1di\x1fu\x1es\x11m\x16r\x0fx", "biusmrx"},
		{"colours", "\x0304red\x03 \x0304,02on blue\x03 \x0312x", "red on blue x"},
		{"hex colours", "\x04ff0000hex\x04 \x04FF0000,00FF00bg", "hex bg"},
		{"keeps newlines and other control characters", "a\x02\nb\x01c\td", "a\nb\x01c\td"},
		{"colour at end", "a\x03", "a"},
		{"comma without foreground", "\x03,5a", ",5a"},
		{"comma without background", "\x034,a", ",a"},
		{"third digit", "\x03123a", "3a"},
		{"short hex", "\x04abcb", "abcb"},
		{"short hex background", "\x04ff0000,abc", ",abc"},
		{"multi-byte text", "\x02héllo\x02 wörld", "héllo wörld"},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, StripFormatting(tt.text))
				assert.Equal(t, tt.want, string(StripFormatting([]byte(tt.text))))
			},
		)
	}
}

func TestFlatten(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"empty", "", ""},
		{"only whitespace", " \n\t ", ""},
		{"newlines become spaces", "one\ntwo\r\nthree", "one two three"},
		{"runs of whitespace collapse", "  a \t\t b  ", "a b"},
		{"formatting removed", "\x02bold\x02 \x0304,02red\x03 \x04ff0000hex", "bold red hex"},
		{"ctcp delimiters removed", "\x01ACTION waves\x01", "ACTION waves"},
		{"other control characters removed", "a\x00b\x07c\x7fd", "abcd"},
		{"code between spaces", "a \x02 b", "a b"},
		{"malformed colour digits kept", "\x03,5a \x04abc", ",5a abc"},
		{"unicode spaces collapse", "a  b", "a b"},
		{"multi-byte text", "héllo\nwörld", "héllo wörld"},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, Flatten(tt.text))
			},
		)
	}
}
