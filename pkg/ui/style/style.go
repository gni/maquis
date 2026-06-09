package style

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"
)

type colorVal struct {
	R, G, B uint8
}

func (c colorVal) RGBA() (r, g, b, a uint32) {
	r = uint32(c.R) * 0x101
	g = uint32(c.G) * 0x101
	b = uint32(c.B) * 0x101
	a = 0xffff
	return
}

func Color(hex string) colorVal {
	if len(hex) > 0 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) == 6 {
		r, _ := strconv.ParseUint(hex[0:2], 16, 8)
		g, _ := strconv.ParseUint(hex[2:4], 16, 8)
		b, _ := strconv.ParseUint(hex[4:6], 16, 8)
		return colorVal{R: uint8(r), G: uint8(g), B: uint8(b)}
	}
	return colorVal{255, 255, 255}
}

const (
	noBorder = iota
	roundedBorder
	normalBorder
)

func RoundedBorder() int {
	return roundedBorder
}

func NormalBorder() int {
	return normalBorder
}

const (
	Center = iota
	Left
	Right
	Top
	Bottom
)

type Style struct {
	fg            *colorVal
	bg            *colorVal
	bold          bool
	italic        bool
	underline     bool
	borderType    int
	borderColor   *colorVal
	paddingLeft   int
	paddingRight  int
	paddingTop    int
	paddingBottom int
	marginLeft    int
	marginRight   int
	marginTop     int
	marginBottom  int
}

func NewStyle() Style {
	return Style{}
}

func (s Style) Foreground(c color.Color) Style {
	if lc, ok := c.(colorVal); ok {
		s.fg = &lc
	} else if c != nil {
		r, g, b, _ := c.RGBA()
		s.fg = &colorVal{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
	return s
}

func (s Style) Background(c color.Color) Style {
	if lc, ok := c.(colorVal); ok {
		s.bg = &lc
	} else if c != nil {
		r, g, b, _ := c.RGBA()
		s.bg = &colorVal{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
	return s
}

func (s Style) Bold(v bool) Style {
	s.bold = v
	return s
}

func (s Style) Italic(v bool) Style {
	s.italic = v
	return s
}

func (s Style) Underline(v bool) Style {
	s.underline = v
	return s
}

func (s Style) MarginLeft(v int) Style {
	s.marginLeft = v
	return s
}

func (s Style) Margin(args ...int) Style {
	if len(args) == 1 {
		s.marginTop = args[0]
		s.marginRight = args[0]
		s.marginBottom = args[0]
		s.marginLeft = args[0]
	} else if len(args) == 2 {
		s.marginTop = args[0]
		s.marginBottom = args[0]
		s.marginLeft = args[1]
		s.marginRight = args[1]
	} else if len(args) == 4 {
		s.marginTop = args[0]
		s.marginRight = args[1]
		s.marginBottom = args[2]
		s.marginLeft = args[3]
	}
	return s
}

func (s Style) Padding(args ...int) Style {
	if len(args) == 1 {
		s.paddingTop = args[0]
		s.paddingRight = args[0]
		s.paddingBottom = args[0]
		s.paddingLeft = args[0]
	} else if len(args) == 2 {
		s.paddingTop = args[0]
		s.paddingBottom = args[0]
		s.paddingLeft = args[1]
		s.paddingRight = args[1]
	} else if len(args) == 4 {
		s.paddingTop = args[0]
		s.paddingRight = args[1]
		s.paddingBottom = args[2]
		s.paddingLeft = args[3]
	}
	return s
}

func (s Style) Border(b interface{}, args ...bool) Style {
	if bt, ok := b.(int); ok {
		s.borderType = bt
	} else {
		s.borderType = roundedBorder
	}
	return s
}

func (s Style) BorderForeground(c color.Color) Style {
	if lc, ok := c.(colorVal); ok {
		s.borderColor = &lc
	} else if c != nil {
		r, g, b, _ := c.RGBA()
		s.borderColor = &colorVal{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
	return s
}

func (s Style) Render(args ...string) string {
	str := strings.Join(args, " ")
	lines := strings.Split(str, "\n")

	ansiStart := ""
	ansiEnd := ""
	if s.bold {
		ansiStart += "\x1b[1m"
	}
	if s.italic {
		ansiStart += "\x1b[3m"
	}
	if s.underline {
		ansiStart += "\x1b[4m"
	}
	if s.fg != nil {
		ansiStart += fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.fg.R, s.fg.G, s.fg.B)
	}
	if s.bg != nil {
		ansiStart += fmt.Sprintf("\x1b[48;2;%d;%d;%dm", s.bg.R, s.bg.G, s.bg.B)
	}
	if ansiStart != "" {
		ansiEnd = "\x1b[0m"
	}

	maxWidth := 0
	for _, line := range lines {
		w := utf8.RuneCountInString(stripAnsi(line))
		if w > maxWidth {
			maxWidth = w
		}
	}

	paddedWidth := maxWidth + s.paddingLeft + s.paddingRight
	var formattedLines []string
	for _, line := range lines {
		runeCount := utf8.RuneCountInString(stripAnsi(line))
		padL := strings.Repeat(" ", s.paddingLeft)
		padR := strings.Repeat(" ", s.paddingRight + (maxWidth - runeCount))
		formattedLines = append(formattedLines, padL + line + padR)
	}

	var borderedLines []string
	borderStart := ""
	borderEnd := ""
	if s.borderColor != nil {
		borderStart = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", s.borderColor.R, s.borderColor.G, s.borderColor.B)
		borderEnd = "\x1b[0m"
	}

	if s.borderType != noBorder {
		var tl, tr, bl, br, h, v string
		if s.borderType == roundedBorder {
			tl, tr, bl, br, h, v = "╭", "╮", "╰", "╯", "─", "│"
		} else {
			tl, tr, bl, br, h, v = "┌", "┐", "└", "┘", "─", "│"
		}

		top := borderStart + tl + strings.Repeat(h, paddedWidth) + tr + borderEnd
		borderedLines = append(borderedLines, top)

		for i := 0; i < s.paddingTop; i++ {
			line := borderStart + v + borderEnd + strings.Repeat(" ", paddedWidth) + borderStart + v + borderEnd
			borderedLines = append(borderedLines, line)
		}

		for _, line := range formattedLines {
			contentLine := borderStart + v + borderEnd + ansiStart + line + ansiEnd + borderStart + v + borderEnd
			borderedLines = append(borderedLines, contentLine)
		}

		for i := 0; i < s.paddingBottom; i++ {
			line := borderStart + v + borderEnd + strings.Repeat(" ", paddedWidth) + borderStart + v + borderEnd
			borderedLines = append(borderedLines, line)
		}

		bottom := borderStart + bl + strings.Repeat(h, paddedWidth) + br + borderEnd
		borderedLines = append(borderedLines, bottom)
	} else {
		for _, line := range formattedLines {
			borderedLines = append(borderedLines, ansiStart + line + ansiEnd)
		}
	}

	var marginLines []string
	for i := 0; i < s.marginTop; i++ {
		marginLines = append(marginLines, "")
	}
	for _, line := range borderedLines {
		marginLines = append(marginLines, strings.Repeat(" ", s.marginLeft) + line + strings.Repeat(" ", s.marginRight))
	}
	for i := 0; i < s.marginBottom; i++ {
		marginLines = append(marginLines, "")
	}

	return strings.Join(marginLines, "\n")
}

func JoinHorizontal(pos int, strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}

	var splitStrs [][]string
	maxLines := 0
	for _, s := range strs {
		lines := strings.Split(s, "\n")
		splitStrs = append(splitStrs, lines)
		if len(lines) > maxLines {
			maxLines = len(lines)
		}
	}

	var widths []int
	for _, lines := range splitStrs {
		maxW := 0
		for _, l := range lines {
			w := utf8.RuneCountInString(stripAnsi(l))
			if w > maxW {
				maxW = w
			}
		}
		widths = append(widths, maxW)
	}

	var joinedLines []string
	for i := 0; i < maxLines; i++ {
		var lineParts []string
		for idx, lines := range splitStrs {
			w := widths[idx]
			var lineVal string
			if i < len(lines) {
				lineVal = lines[i]
			}
			visibleW := utf8.RuneCountInString(stripAnsi(lineVal))
			padR := strings.Repeat(" ", w - visibleW)
			lineParts = append(lineParts, lineVal + padR)
		}
		joinedLines = append(joinedLines, strings.Join(lineParts, ""))
	}

	return strings.Join(joinedLines, "\n")
}

func JoinVertical(pos int, strs ...string) string {
	return strings.Join(strs, "\n")
}

func stripAnsi(str string) string {
	var sb strings.Builder
	inEscape := false
	for i := 0; i < len(str); i++ {
		if str[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if str[i] == 'm' {
				inEscape = false
			}
			continue
		}
		sb.WriteByte(str[i])
	}
	return sb.String()
}
