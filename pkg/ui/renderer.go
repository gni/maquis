package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/chroma/v2/quick"
	"maquis/pkg/ui/style"
)

type StreamRenderer struct {
	mu                        sync.Mutex
	w                         io.Writer
	theme                     UITheme
	inCodeBlock               bool
	inThinking                bool
	showThinking              bool
	reasoningStart            time.Time
	reasoningHasText          bool
	reasoningEndedWithNewline bool
	reasoningDuration         float64
	reasoningResetSequence    string
	pendingThoughtTextGap     bool

	lastEndedWithNewline bool
	hasWrittenText       bool
	hasWrittenThoughts   bool
	parser               *jsonStreamParser
	live                 *liveMarkdownRenderer
}

func findPromptPreservingWriter(w io.Writer) *PromptPreservingWriter {
	for w != nil {
		if ppw, ok := w.(*PromptPreservingWriter); ok {
			return ppw
		}
		if unwrapper, ok := w.(interface{ Unwrap() io.Writer }); ok {
			w = unwrapper.Unwrap()
		} else {
			break
		}
	}
	return nil
}

func NewStreamRenderer(w io.Writer, theme UITheme, showThinking bool, streamWrites bool, _ string) *StreamRenderer {
	sr := &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		lastEndedWithNewline: true,
		live:                 newLiveMarkdownRenderer(w, theme),
	}
	sr.parser = &jsonStreamParser{
		streamWrites: streamWrites,
	}
	return sr
}

func (sr *StreamRenderer) HasOutput() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.hasWrittenText || sr.hasWrittenThoughts
}

func (sr *StreamRenderer) printReasoningLine(line string) {
	dimStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)
	fmt.Fprint(sr.w, dimStyle.Render(line))
}

func (sr *StreamRenderer) WriteReasoning(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.showThinking || chunk == "" {
		return
	}
	sr.checkFirstWrite()

	if !sr.inThinking {
		sr.inThinking = true
		sr.reasoningStart = time.Now()
		sr.reasoningHasText = false
		sr.reasoningEndedWithNewline = false
		sr.pendingThoughtTextGap = false

		dimStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)
		startSeq, resetSeq := dimStyle.GetSequence()
		fmt.Fprint(sr.w, startSeq)
		sr.reasoningResetSequence = resetSeq
	}

	sr.hasWrittenThoughts = true
	sr.reasoningHasText = true
	sr.reasoningEndedWithNewline = strings.HasSuffix(chunk, "\n")
	fmt.Fprint(sr.w, chunk)
}

func (sr *StreamRenderer) EndThinking() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()
}

func (sr *StreamRenderer) endThinking() {
	if !sr.inThinking {
		return
	}
	sr.inThinking = false

	if sr.reasoningResetSequence != "" {
		fmt.Fprint(sr.w, sr.reasoningResetSequence)
		sr.reasoningResetSequence = ""
	}
	if !sr.reasoningHasText {
		return
	}
	if !sr.reasoningEndedWithNewline {
		fmt.Fprint(sr.w, "\n")
	}

	elapsed := time.Since(sr.reasoningStart).Seconds()
	sr.reasoningDuration = elapsed
	iconStyle := style.NewStyle().Foreground(sr.theme.Success)
	labelStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)

	fmt.Fprintf(sr.w, "%s %s\n",
		iconStyle.Render("✔"),
		labelStyle.Render(fmt.Sprintf("thought (%.1fs)", elapsed)),
	)
	sr.reasoningHasText = false
	sr.reasoningEndedWithNewline = true
	sr.pendingThoughtTextGap = true
	sr.lastEndedWithNewline = true
	sr.live.ResetAtLineStart()
}

func (sr *StreamRenderer) Write(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()

	if chunk == "" {
		return
	}

	if sr.pendingThoughtTextGap {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	sr.checkFirstWrite()
	sr.hasWrittenText = true
	sr.live.Write(chunk)
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
}

func (sr *StreamRenderer) finishLiveTextLocked() {
	if sr.hasWrittenText {
		sr.live.EnsureTrailingNewline()
	} else {
		sr.live.Flush()
	}
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
}

func (sr *StreamRenderer) Flush() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.flushLocked()
}

func (sr *StreamRenderer) flushLocked() {
	sr.endThinking()
	sr.pendingThoughtTextGap = false
	sr.finishLiveTextLocked()
	sr.flushActiveToolLocked()
}

func (sr *StreamRenderer) flushActiveToolLocked() {
	if sr.parser != nil && sr.parser.activeToolName != "" {
		if !sr.parser.titlePrinted {
			sr.parser.printStreamTitle(sr.w, sr.theme)
		}
		if sr.parser.outputBuf.Len() > 0 {
			fmt.Fprint(sr.w, sr.parser.outputBuf.String())
			sr.parser.outputBuf.Reset()
		}
		if sr.parser.lineBuffer.Len() > 0 {
			lang := sr.parser.guessedLang
			if lang == "" {
				lang = "plaintext"
			}
			_ = HighlightWithoutTrailingNewline(sr.w, sr.parser.lineBuffer.String(), lang, sr.theme.ChromaStyle)
			fmt.Fprint(sr.w, "\n")
			sr.parser.lineBuffer.Reset()
		}
	}
}

func (sr *StreamRenderer) printNormalLine(line string) {
	trimmed := strings.TrimSpace(line)

	// 0. Handle delimiter: make a space instead of "----"
	if trimmed == "----" {
		fmt.Fprint(sr.w, " ")
		return
	}

	// 1. Handle headers: e.g. "# Header", "## Header", etc.
	if strings.HasPrefix(trimmed, "#") {
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		headerText := strings.TrimSpace(trimmed[hashes:])
		var styled string
		if hashes == 1 {
			styled = style.NewStyle().Foreground(sr.theme.Secondary).Bold(true).Underline(true).Render(headerText)
		} else {
			styled = style.NewStyle().Foreground(sr.theme.Secondary).Bold(true).Render(headerText)
		}
		fmt.Fprint(sr.w, styled)
		return
	}

	// 2. Handle blockquotes: e.g. "> text"
	if strings.HasPrefix(trimmed, ">") {
		quoteText := strings.TrimSpace(trimmed[1:])
		styled := style.NewStyle().Foreground(sr.theme.Border).Italic(true).Render("┃ " + quoteText)
		fmt.Fprint(sr.w, styled)
		return
	}

	// 3. Handle bullet points: e.g. "- item" or "* item"
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "• ") {
		bulletText := trimmed[2:]
		bulletSymbol := style.NewStyle().Foreground(sr.theme.Primary).Render("•")
		fmt.Fprintf(sr.w, "  %s %s", bulletSymbol, sr.renderInlineMarkdown(bulletText))
		return
	}

	// 3.5 Handle numbered lists: e.g. "1. item"
	if ok, numPrefix, listText := isNumberedList(trimmed); ok {
		numSymbol := style.NewStyle().Foreground(sr.theme.Primary).Render(numPrefix)
		fmt.Fprintf(sr.w, "  %s %s", numSymbol, sr.renderInlineMarkdown(listText))
		return
	}

	// 4. Standard text line: parse inline styles (bold, code, italic)
	fmt.Fprint(sr.w, sr.renderInlineMarkdown(line))
}

func (sr *StreamRenderer) renderInlineMarkdown(text string) string {
	var result strings.Builder
	runes := []rune(text)
	n := len(runes)

	for i := 0; i < n; {
		// 1. Inline code: `code`
		if runes[i] == '`' {
			j := i + 1
			for j < n && runes[j] != '`' {
				j++
			}
			if j < n {
				codeVal := string(runes[i+1 : j])
				var styled string
				if sr.inThinking {
					styled = style.NewStyle().Foreground(sr.theme.Border).Underline(true).Italic(true).Render(codeVal)
				} else {
					styled = style.NewStyle().Foreground(sr.theme.Highlight).Render(codeVal)
				}
				result.WriteString(styled)
				i = j + 1
				continue
			}
		}

		// 2. Bold: **bold**
		if i+1 < n && runes[i] == '*' && runes[i+1] == '*' {
			j := i + 2
			found := false
			for j+1 < n {
				if runes[j] == '*' && runes[j+1] == '*' {
					found = true
					break
				}
				j++
			}
			if found {
				boldVal := string(runes[i+2 : j])
				var styled string
				if sr.inThinking {
					styled = style.NewStyle().Foreground(sr.theme.Border).Bold(true).Italic(true).Render(sr.renderInlineMarkdown(boldVal))
				} else {
					styled = style.NewStyle().Foreground(sr.theme.Primary).Bold(true).Render(sr.renderInlineMarkdown(boldVal))
				}
				result.WriteString(styled)
				i = j + 2
				continue
			}
		}

		// 3. Italic: *italic*
		if runes[i] == '*' {
			j := i + 1
			for j < n && runes[j] != '*' {
				j++
			}
			if j < n {
				italicVal := string(runes[i+1 : j])
				var styled string
				if sr.inThinking {
					styled = style.NewStyle().Foreground(sr.theme.Border).Italic(true).Render(sr.renderInlineMarkdown(italicVal))
				} else {
					styled = style.NewStyle().Italic(true).Render(sr.renderInlineMarkdown(italicVal))
				}
				result.WriteString(styled)
				i = j + 1
				continue
			}
		}

		result.WriteRune(runes[i])
		i++
	}

	return result.String()
}

func HighlightWithoutTrailingNewline(w io.Writer, source, lang, chromaStyle string) error {
	if chromaStyle == "" {
		chromaStyle = "friendly"
	}
	var buf bytes.Buffer
	err := quick.Highlight(&buf, source, lang, "terminal16", chromaStyle)
	if err != nil {
		return err
	}
	data := buf.Bytes()
	if !strings.Contains(source, "\n") {
		var stripped []byte
		for _, b := range data {
			if b != '\n' && b != '\r' {
				stripped = append(stripped, b)
			}
		}
		data = stripped
	}
	_, err = w.Write(data)
	return err
}

func (sr *StreamRenderer) StartToolCall(toolName string, toolCallIndex int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	sr.endThinking()
	sr.pendingThoughtTextGap = false
	sr.finishLiveTextLocked()

	if sr.parser != nil {
		if sr.parser.activeToolName != "" && sr.parser.activeToolIndex == toolCallIndex {
			sr.parser.activeToolName = toolName
			return
		}
		if sr.parser.activeToolName != "" {
			sr.flushActiveToolLocked()
		}
		sr.parser.activeToolIndex = toolCallIndex
		sr.parser.ensureTrackingIndex()
		sr.parser.toolTitleLineNumbers[toolCallIndex] = -1
		sr.parser.toolBodyStreamed[toolCallIndex] = false
		sr.parser.activeToolName = toolName
		sr.parser.titlePrinted = false
		sr.parser.path = ""
		sr.parser.pathPrinted = false
		sr.parser.isContent = false
		sr.parser.isPath = false
		sr.parser.outputBuf.Reset()
		sr.parser.lineBuffer.Reset()
		sr.parser.inString = false
		sr.parser.inEscape = false
		sr.parser.currentKey = ""
		sr.parser.inValue = false
		sr.parser.buf.Reset()
	}
}

func (sr *StreamRenderer) WriteToolCall(content string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if len(content) > 0 {
		sr.checkFirstWrite()
	}

	if sr.parser != nil {
		sr.parser.feed(content, sr.w, sr.theme)
	}
}

func (sr *StreamRenderer) checkFirstWrite() {
	if getUI().PasteLinesOffset > 0 {
		getUI().PasteLinesOffset = 0
		InitStatusBar(os.Stderr)
	}
}

func (sr *StreamRenderer) GetToolTitleLineNumber(index int) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.toolTitleLineNumbers) {
		return -1
	}
	return sr.parser.toolTitleLineNumbers[index]
}

func (sr *StreamRenderer) DidStreamToolBody(index int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.parser != nil && index >= 0 && index < len(sr.parser.toolBodyStreamed) && sr.parser.toolBodyStreamed[index]
}

func (sr *StreamRenderer) CompleteToolCall(index int, toolName string, toolArgs string, isError bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.toolTitleLineNumbers) {
		return
	}
	status := toolStatusSuccess
	if isError {
		status = toolStatusError
	}
	symbol := renderToolSymbol(toolName, status, sr.theme)
	target := extractToolTarget(toolName, toolArgs)
	replaceTrackedStreamLine(sr.w, sr.parser.toolTitleLineNumbers[index], FormatToolTitle(symbol, toolName, target, sr.theme))
}

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}

func isNumberedList(trimmed string) (bool, string, string) {
	if len(trimmed) < 3 {
		return false, "", ""
	}
	spIdx := strings.Index(trimmed, " ")
	if spIdx == -1 {
		return false, "", ""
	}
	prefix := trimmed[:spIdx]
	if len(prefix) < 2 || !strings.HasSuffix(prefix, ".") {
		return false, "", ""
	}
	numPart := prefix[:len(prefix)-1]
	for _, r := range numPart {
		if r < '0' || r > '9' {
			return false, "", ""
		}
	}
	return true, prefix, trimmed[spIdx+1:]
}

func wrapMarkdownLine(line string, width int) []string {
	if width <= 10 {
		return []string{line}
	}
	trimmed := strings.TrimSpace(line)
	var prefix string
	var content string

	if strings.HasPrefix(trimmed, ">") {
		prefix = "> "
		content = strings.TrimSpace(trimmed[1:])
	} else if strings.HasPrefix(trimmed, "- ") {
		prefix = "- "
		content = trimmed[2:]
	} else if strings.HasPrefix(trimmed, "* ") {
		prefix = "* "
		content = trimmed[2:]
	} else if strings.HasPrefix(trimmed, "• ") {
		prefix = "• "
		content = trimmed[2:]
	} else if ok, numPrefix, listText := isNumberedList(trimmed); ok {
		prefix = numPrefix + " "
		content = listText
	} else {
		content = line
	}

	var leadingSpaces string
	if prefix == "" {
		for _, r := range line {
			if r == ' ' {
				leadingSpaces += " "
			} else {
				break
			}
		}
	}

	words := strings.Split(content, " ")
	var lines []string
	var currentLine strings.Builder

	effectiveWidth := width - len(prefix) - len(leadingSpaces)
	if effectiveWidth < 15 {
		effectiveWidth = 15
	}

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= effectiveWidth {
			currentLine.WriteByte(' ')
			currentLine.WriteString(word)
		} else {
			lines = append(lines, prefix+leadingSpaces+currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, prefix+leadingSpaces+currentLine.String())
	}
	if len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}
