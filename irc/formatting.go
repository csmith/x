// Package irc handles IRC text, such as its formatting codes: bold, italic,
// underline, strikethrough, monospace, reverse, reset, and colours in both the
// numbered (\x03) and hex (\x04) forms.
package irc

import (
	"strings"
	"unicode"
)

const (
	codeBold          = '\x02'
	codeColour        = '\x03'
	codeHexColour     = '\x04'
	codeReset         = '\x0f'
	codeMonospace     = '\x11'
	codeReverse       = '\x16'
	codeItalic        = '\x1d'
	codeStrikethrough = '\x1e'
	codeUnderline     = '\x1f'
)

// Style is the formatting in effect for a run of text. Colours are lowercase
// CSS hex colours such as "#ff0000", or empty for the client's default.
type Style struct {
	Bold, Italic, Underline, Strikethrough, Monospace, Reverse bool

	Foreground, Background string
}

// Span is a run of text with one style.
type Span struct {
	Style
	Text string
}

// ParseFormatting splits text into spans at its formatting codes, leaving the
// codes out. Adjacent runs with the same style are merged, so text without
// codes is a single unstyled span. Empty text gives no spans.
func ParseFormatting(text string) []Span {
	var (
		spans []Span
		style Style
	)
	for len(text) > 0 {
		i := indexCode(text)
		if i < 0 {
			i = len(text)
		}
		if i > 0 {
			if n := len(spans); n > 0 && spans[n-1].Style == style {
				spans[n-1].Text += text[:i]
			} else {
				spans = append(spans, Span{Style: style, Text: text[:i]})
			}
			text = text[i:]
			continue
		}

		n := codeLen(text)
		code := text[:n]
		text = text[n:]
		switch code[0] {
		case codeBold:
			style.Bold = !style.Bold
		case codeItalic:
			style.Italic = !style.Italic
		case codeUnderline:
			style.Underline = !style.Underline
		case codeStrikethrough:
			style.Strikethrough = !style.Strikethrough
		case codeMonospace:
			style.Monospace = !style.Monospace
		case codeReverse:
			style.Reverse = !style.Reverse
		case codeReset:
			style = Style{}
		case codeColour, codeHexColour:
			fg, bg, hasBG := strings.Cut(code[1:], ",")
			if fg == "" {
				style.Foreground, style.Background = "", ""
				continue
			}
			style.Foreground = colour(code[0], fg)
			if hasBG {
				style.Background = colour(code[0], bg)
			}
		}
	}
	return spans
}

// StripFormatting removes formatting codes from text, leaving what a client
// would display. Other control characters, including newlines, are kept. Text
// without any codes is returned as is, so a byte slice result may share its
// backing array with the input.
func StripFormatting[T string | []byte](text T) T {
	i := indexCode(text)
	if i < 0 {
		return text
	}
	res := make([]byte, 0, len(text))
	for i >= 0 {
		res = append(res, text[:i]...)
		text = text[i+codeLen(text[i:]):]
		i = indexCode(text)
	}
	return T(append(res, text...))
}

// Flatten reduces text to a single clean line: formatting codes and other
// control characters (such as CTCP delimiters) are removed, and runs of
// whitespace, including newlines, become a single space with none at either
// end.
func Flatten(text string) string {
	text = strings.Map(
		func(r rune) rune {
			if unicode.IsControl(r) && !unicode.IsSpace(r) {
				return -1
			}
			return r
		},
		StripFormatting(text),
	)
	return strings.Join(strings.Fields(text), " ")
}

// indexCode finds the first formatting code in s. Codes are all ASCII, which
// never appears inside a multi-byte UTF-8 sequence, so a byte scan is safe.
func indexCode[T string | []byte](s T) int {
	for i := range len(s) {
		if isCode(s[i]) {
			return i
		}
	}
	return -1
}

func isCode(c byte) bool {
	switch c {
	case codeBold, codeColour, codeHexColour, codeReset, codeMonospace,
		codeReverse, codeItalic, codeStrikethrough, codeUnderline:
		return true
	}
	return false
}

// codeLen is the length of the formatting code at the start of s, including
// any colours that follow a colour code. Numbered colours are one or two
// digits; hex colours must be exactly six hex digits or the code stands alone.
// A comma is only part of the code when a valid background colour follows it.
func codeLen[T string | []byte](s T) int {
	var digit func(byte) bool
	var width int
	switch s[0] {
	case codeColour:
		digit, width = isDigit, 2
	case codeHexColour:
		digit, width = isHexDigit, 6
	default:
		return 1
	}

	colourLen := func(s T) int {
		n := 0
		for n < len(s) && n < width && digit(s[n]) {
			n++
		}
		if width == 6 && n != 6 {
			return 0
		}
		return n
	}

	n := 1 + colourLen(s[1:])
	if n == 1 {
		return n
	}
	if n < len(s) && s[n] == ',' {
		if bg := colourLen(s[n+1:]); bg > 0 {
			n += 1 + bg
		}
	}
	return n
}

func colour(code byte, value string) string {
	if code == codeHexColour {
		return "#" + strings.ToLower(value)
	}
	// Up to two digits are read, so "04" and "4" are both red.
	i := 0
	for _, c := range []byte(value) {
		i = i*10 + int(c-'0')
	}
	if i >= len(palette) {
		// 99 is the client's default colour.
		return ""
	}
	return palette[i]
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// palette maps colour numbers to RGB: mIRC's defaults for 0 to 15, then the
// extended colours.
var palette = [...]string{
	"#ffffff", "#000000", "#00007f", "#009300", "#ff0000", "#7f0000", "#9c009c", "#fc7f00",
	"#ffff00", "#00fc00", "#009393", "#00ffff", "#0000fc", "#ff00ff", "#7f7f7f", "#d2d2d2",
	"#470000", "#472100", "#474700", "#324700", "#004700", "#00472c", "#004747", "#002747",
	"#000047", "#2e0047", "#470047", "#47002a", "#740000", "#743a00", "#747400", "#517400",
	"#007400", "#007449", "#007474", "#004074", "#000074", "#4b0074", "#740074", "#740045",
	"#b50000", "#b56300", "#b5b500", "#7db500", "#00b500", "#00b571", "#00b5b5", "#0063b5",
	"#0000b5", "#7500b5", "#b500b5", "#b5006b", "#ff0000", "#ff8c00", "#ffff00", "#b2ff00",
	"#00ff00", "#00ffa0", "#00ffff", "#008cff", "#0000ff", "#a500ff", "#ff00ff", "#ff0098",
	"#ff5959", "#ffb459", "#ffff71", "#cfff60", "#6fff6f", "#65ffc9", "#6dffff", "#59b4ff",
	"#5959ff", "#c459ff", "#ff66ff", "#ff59bc", "#ff9c9c", "#ffd39c", "#ffff9c", "#e2ff9c",
	"#9cff9c", "#9cffdb", "#9cffff", "#9cd3ff", "#9c9cff", "#dc9cff", "#ff9cff", "#ff94d3",
	"#000000", "#131313", "#282828", "#363636", "#4d4d4d", "#656565", "#818181", "#9f9f9f",
	"#bcbcbc", "#e2e2e2", "#ffffff",
}
