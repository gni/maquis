package ui

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"unicode"

	"maquis/pkg/ui/style"
)

const (
	maxLiveFenceHeaderBytes = 128
	maxLiveCodeLineBytes    = 256 * 1024
)

type liveMarkdownBlock uint8

const (
	liveBlockNone liveMarkdownBlock = iota
	liveBlockHeading
	liveBlockQuote
)

type livePrefixKind uint8

const (
	livePrefixPending livePrefixKind = iota
	livePrefixPlain
	livePrefixFence
	livePrefixHeading
	livePrefixQuote
	livePrefixBullet
	livePrefixNumbered
)

type livePrefixDecision struct {
	kind         livePrefixKind
	headingLevel int
	marker       string
}

type liveMarkdownRenderer struct {
	w     io.Writer
	theme UITheme

	atLineStart bool
	prefix      strings.Builder

	inFenceHeader bool
	fenceHeader   strings.Builder
	inCodeBlock   bool
	codeLanguage  string
	codeLine      strings.Builder
	codeOverflow  bool

	block        liveMarkdownBlock
	headingLevel int
	bold         bool
	italic       bool
	inlineCode   bool
	pendingStar  bool
	escaped      bool
	styleActive  bool

	endedWithNewline bool
}

func newLiveMarkdownRenderer(w io.Writer, theme UITheme) *liveMarkdownRenderer {
	return &liveMarkdownRenderer{
		w:                w,
		theme:            theme,
		atLineStart:      true,
		endedWithNewline: true,
	}
}

func (r *liveMarkdownRenderer) Write(chunk string) {
	var plain strings.Builder
	for _, current := range chunk {
		r.writeRune(current, &plain)
	}
	r.flushPlain(&plain)
}

func (r *liveMarkdownRenderer) writeRune(current rune, plain *strings.Builder) {
	if r.inFenceHeader {
		r.writeFenceHeaderRune(current, plain)
		return
	}
	if r.inCodeBlock {
		r.writeCodeRune(current, plain)
		return
	}
	if r.atLineStart {
		r.prefix.WriteRune(current)
		decision := classifyLivePrefix(r.prefix.String())
		switch decision.kind {
		case livePrefixPending:
			return
		case livePrefixFence:
			r.resetInlineStyles(plain)
			r.inFenceHeader = true
			r.atLineStart = false
			return
		case livePrefixHeading:
			r.prefix.Reset()
			r.atLineStart = false
			r.block = liveBlockHeading
			r.headingLevel = decision.headingLevel
			r.transitionStyle(plain)
			return
		case livePrefixQuote:
			r.prefix.Reset()
			r.atLineStart = false
			r.emitStyledMarker("┃ ", r.theme.Border, plain)
			r.block = liveBlockQuote
			r.transitionStyle(plain)
			return
		case livePrefixBullet:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			fmt.Fprintf(r.w, "  %s ", style.NewStyle().Foreground(r.theme.Primary).Render("•"))
			r.endedWithNewline = false
			return
		case livePrefixNumbered:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			number := style.NewStyle().Foreground(r.theme.Primary).Render(decision.marker)
			fmt.Fprintf(r.w, "  %s ", number)
			r.endedWithNewline = false
			return
		case livePrefixPlain:
			prefix := r.prefix.String()
			r.prefix.Reset()
			r.atLineStart = false
			for _, prefixRune := range prefix {
				r.writeInlineRune(prefixRune, plain)
			}
			return
		}
	}

	r.writeInlineRune(current, plain)
}

func (r *liveMarkdownRenderer) writeFenceHeaderRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		language := "plaintext"
		if fields := strings.Fields(r.fenceHeader.String()); len(fields) > 0 {
			language = fields[0]
		}
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
		r.inCodeBlock = true
		r.codeLanguage = language
		r.atLineStart = true
		return
	}

	r.fenceHeader.WriteRune(current)
	if r.fenceHeader.Len() <= maxLiveFenceHeaderBytes {
		return
	}

	r.resetInlineStyles(plain)
	plain.WriteString(r.prefix.String())
	plain.WriteString(r.fenceHeader.String())
	r.prefix.Reset()
	r.fenceHeader.Reset()
	r.inFenceHeader = false
	r.atLineStart = false
}

func (r *liveMarkdownRenderer) writeCodeRune(current rune, plain *strings.Builder) {
	if r.codeOverflow {
		plain.WriteRune(current)
		if current == '\n' {
			r.codeOverflow = false
			r.atLineStart = true
		}
		return
	}

	if current == '\n' {
		line := r.codeLine.String()
		r.codeLine.Reset()
		if isLiveCodeFence(line) {
			r.inCodeBlock = false
			r.codeLanguage = ""
			r.atLineStart = true
			return
		}
		r.flushPlain(plain)
		r.writeHighlightedCodeLine(line, true)
		r.atLineStart = true
		return
	}

	r.codeLine.WriteRune(current)
	if r.codeLine.Len() <= maxLiveCodeLineBytes {
		return
	}

	r.flushPlain(plain)
	r.emitVisible(r.codeLine.String())
	r.codeLine.Reset()
	r.codeOverflow = true
}

func (r *liveMarkdownRenderer) writeInlineRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		r.finishInlineLine(plain)
		plain.WriteRune('\n')
		r.atLineStart = true
		return
	}

	if r.escaped {
		if !strings.ContainsRune(`\\`+"`*_{}[]()#+-.!>", current) {
			plain.WriteRune('\\')
		}
		plain.WriteRune(current)
		r.escaped = false
		return
	}

	if !r.inlineCode && current == '\\' {
		r.escaped = true
		return
	}

	if current == '`' {
		r.resolvePendingStar(plain, false)
		r.inlineCode = !r.inlineCode
		r.transitionStyle(plain)
		return
	}

	if r.inlineCode {
		plain.WriteRune(current)
		return
	}

	if current == '*' {
		if r.pendingStar {
			r.pendingStar = false
			r.bold = !r.bold
			r.transitionStyle(plain)
		} else {
			r.pendingStar = true
		}
		return
	}

	if r.pendingStar && !r.italic && unicode.IsSpace(current) {
		r.pendingStar = false
		plain.WriteRune('*')
	} else {
		r.resolvePendingStar(plain, false)
	}
	plain.WriteRune(current)
}

func (r *liveMarkdownRenderer) resolvePendingStar(plain *strings.Builder, final bool) {
	if !r.pendingStar {
		return
	}
	r.pendingStar = false
	if r.italic {
		r.italic = false
		r.transitionStyle(plain)
		return
	}
	if final {
		plain.WriteRune('*')
		return
	}
	r.italic = true
	r.transitionStyle(plain)
}

func (r *liveMarkdownRenderer) finishInlineLine(plain *strings.Builder) {
	if r.escaped {
		plain.WriteRune('\\')
		r.escaped = false
	}
	r.resolvePendingStar(plain, true)
	r.block = liveBlockNone
	r.headingLevel = 0
	r.resetInlineStyles(plain)
}

func (r *liveMarkdownRenderer) resetInlineStyles(plain *strings.Builder) {
	r.bold = false
	r.italic = false
	r.inlineCode = false
	r.pendingStar = false
	r.transitionStyle(plain)
}

func (r *liveMarkdownRenderer) transitionStyle(plain *strings.Builder) {
	r.flushPlain(plain)
	if r.styleActive {
		fmt.Fprint(r.w, "\x1b[0m")
		r.styleActive = false
	}

	current := style.NewStyle()
	switch r.block {
	case liveBlockHeading:
		current = current.Foreground(r.theme.Secondary).Bold(true)
		if r.headingLevel == 1 {
			current = current.Underline(true)
		}
	case liveBlockQuote:
		current = current.Foreground(r.theme.Border).Italic(true)
	}
	if r.inlineCode {
		current = current.Foreground(r.theme.Highlight)
	}
	if r.bold {
		current = current.Bold(true)
	}
	if r.italic {
		current = current.Italic(true)
	}

	start, _ := current.GetSequence()
	if start != "" {
		fmt.Fprint(r.w, start)
		r.styleActive = true
	}
}

func (r *liveMarkdownRenderer) emitStyledMarker(marker string, markerColor color.Color, plain *strings.Builder) {
	r.flushPlain(plain)
	fmt.Fprint(r.w, style.NewStyle().Foreground(markerColor).Render(marker))
	r.endedWithNewline = false
}

func (r *liveMarkdownRenderer) writeHighlightedCodeLine(line string, newline bool) {
	language := r.codeLanguage
	if language == "" {
		language = "plaintext"
	}
	if err := HighlightWithoutTrailingNewline(r.w, line, language, r.theme.ChromaStyle); err != nil {
		fmt.Fprint(r.w, line)
	}
	if newline {
		fmt.Fprint(r.w, "\n")
	}
	r.endedWithNewline = newline
}

func (r *liveMarkdownRenderer) flushPlain(plain *strings.Builder) {
	if plain.Len() == 0 {
		return
	}
	r.emitVisible(plain.String())
	plain.Reset()
}

func (r *liveMarkdownRenderer) emitVisible(content string) {
	if content == "" {
		return
	}
	fmt.Fprint(r.w, content)
	r.endedWithNewline = strings.HasSuffix(content, "\n")
}

func (r *liveMarkdownRenderer) Flush() {
	var plain strings.Builder

	if r.inFenceHeader {
		r.resetInlineStyles(&plain)
		plain.WriteString(r.prefix.String())
		plain.WriteString(r.fenceHeader.String())
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
	}

	if r.prefix.Len() > 0 {
		prefix := r.prefix.String()
		r.prefix.Reset()
		r.atLineStart = false
		for _, prefixRune := range prefix {
			r.writeInlineRune(prefixRune, &plain)
		}
	}

	if r.inCodeBlock && !r.codeOverflow && r.codeLine.Len() > 0 {
		line := r.codeLine.String()
		if !isLiveCodeFence(line) {
			r.flushPlain(&plain)
			r.writeHighlightedCodeLine(line, false)
		}
	}

	r.codeLine.Reset()
	r.codeOverflow = false
	r.inCodeBlock = false
	r.codeLanguage = ""
	r.finishInlineLine(&plain)
	r.flushPlain(&plain)
	r.atLineStart = r.endedWithNewline
}

func (r *liveMarkdownRenderer) EnsureTrailingNewline() {
	r.Flush()
	if r.endedWithNewline {
		return
	}
	r.emitVisible("\n")
	r.atLineStart = true
}

func (r *liveMarkdownRenderer) ResetAtLineStart() {
	r.prefix.Reset()
	r.fenceHeader.Reset()
	r.codeLine.Reset()
	r.inFenceHeader = false
	r.inCodeBlock = false
	r.codeOverflow = false
	r.codeLanguage = ""
	r.block = liveBlockNone
	r.headingLevel = 0
	r.bold = false
	r.italic = false
	r.inlineCode = false
	r.pendingStar = false
	r.escaped = false
	r.styleActive = false
	r.atLineStart = true
	r.endedWithNewline = true
}

func (r *liveMarkdownRenderer) EndedWithNewline() bool {
	return r.endedWithNewline
}

func isLiveCodeFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

func classifyLivePrefix(prefix string) livePrefixDecision {
	leadingSpaces := 0
	for leadingSpaces < len(prefix) && prefix[leadingSpaces] == ' ' {
		leadingSpaces++
	}
	if leadingSpaces > 3 {
		return livePrefixDecision{kind: livePrefixPlain}
	}

	body := prefix[leadingSpaces:]
	if body == "" {
		return livePrefixDecision{kind: livePrefixPending}
	}
	if strings.ContainsAny(body, "\r\n") {
		return livePrefixDecision{kind: livePrefixPlain}
	}

	switch body[0] {
	case '`':
		ticks := 0
		for ticks < len(body) && body[ticks] == '`' {
			ticks++
		}
		if ticks == len(body) {
			if ticks < 3 {
				return livePrefixDecision{kind: livePrefixPending}
			}
			if ticks == 3 {
				return livePrefixDecision{kind: livePrefixFence}
			}
		}
	case '#':
		hashes := 0
		for hashes < len(body) && body[hashes] == '#' {
			hashes++
		}
		if hashes == len(body) && hashes <= 6 {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if hashes >= 1 && hashes <= 6 && len(body) == hashes+1 && body[hashes] == ' ' {
			return livePrefixDecision{kind: livePrefixHeading, headingLevel: hashes}
		}
	case '>':
		if body == ">" {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if body == "> " {
			return livePrefixDecision{kind: livePrefixQuote}
		}
	case '-', '+', '*':
		if len(body) == 1 {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if len(body) == 2 && body[1] == ' ' {
			return livePrefixDecision{kind: livePrefixBullet}
		}
	default:
		if body[0] >= '0' && body[0] <= '9' {
			digits := 0
			for digits < len(body) && body[digits] >= '0' && body[digits] <= '9' {
				digits++
			}
			if digits == len(body) && digits <= 9 {
				return livePrefixDecision{kind: livePrefixPending}
			}
			if digits <= 9 && digits < len(body) && body[digits] == '.' {
				if len(body) == digits+1 {
					return livePrefixDecision{kind: livePrefixPending}
				}
				if len(body) == digits+2 && body[digits+1] == ' ' {
					return livePrefixDecision{
						kind:   livePrefixNumbered,
						marker: body[:digits+1],
					}
				}
			}
		}
	}

	return livePrefixDecision{kind: livePrefixPlain}
}
