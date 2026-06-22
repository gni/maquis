package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"maquis/pkg/ui/style"
	"github.com/alecthomas/chroma/v2/quick"
)

type StreamRenderer struct {
	mu               sync.Mutex
	w                io.Writer
	theme            UITheme
	lineBuffer       strings.Builder
	codeBuffer       strings.Builder
	inCodeBlock      bool
	inThinking       bool
	codeLanguage     string
	showThinking     bool
	showFullThinking bool
	reasoningStart    time.Time
	reasoningText     strings.Builder
	reasoningDuration float64
	reasoningResetSequence string
	spinnerFrame      int

	// Streaming states for real-time text formatting
	lineIsHeader bool
	inBold       bool
	inInlineCode bool

	lastEndedWithNewline bool
	hasWrittenText       bool
	hasWrittenThoughts   bool
	parser               *jsonStreamParser
	streamWrites         bool
	agentName            string

	lineStartLine        int
	lineStartCol         int
	lineStartScroll      int
	hasLineStart         bool
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

func clearScrollRegionLines(ppw *PromptPreservingWriter, startLine int, w io.Writer) {
	scrollBottom := ppw.Height() - 5
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	for l := startLine; l <= scrollBottom; l++ {
		ppw.SetPrintLine(l)
		ppw.SetPrintCol(1)
		fmt.Fprint(w, "\x1b[2K")
	}
}

func NewStreamRenderer(w io.Writer, theme UITheme, showThinking bool, streamWrites bool, agentName string) *StreamRenderer {
	return &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		showFullThinking:     true, // Thinking is always full/expanded
		lastEndedWithNewline: true,
		streamWrites:         streamWrites,
		agentName:            agentName,
	}
}

func (sr *StreamRenderer) HasOutput() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.hasWrittenText || sr.hasWrittenThoughts
}

func (sr *StreamRenderer) WriteReasoning(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.showThinking {
		return
	}
	if !sr.inThinking {
		sr.inThinking = true
		sr.reasoningStart = time.Now()
		sr.reasoningText.Reset()

		dimStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)
		startSeq, resetSeq := dimStyle.GetSequence()
		fmt.Fprint(sr.w, startSeq)
		sr.reasoningResetSequence = resetSeq
	}

	if len(chunk) > 0 {
		sr.hasWrittenThoughts = true
	}

	sr.reasoningText.WriteString(chunk)
	fmt.Fprint(sr.w, chunk)
}

func (sr *StreamRenderer) EndThinking() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()
}

func (sr *StreamRenderer) endThinking() {
	if sr.inThinking {
		sr.inThinking = false
		if sr.reasoningText.Len() > 0 {
			if sr.reasoningResetSequence != "" {
				fmt.Fprint(sr.w, sr.reasoningResetSequence)
				sr.reasoningResetSequence = ""
			}
			fmt.Fprint(sr.w, "\n")

			elapsed := time.Since(sr.reasoningStart).Seconds()
			sr.reasoningDuration = elapsed
			iconStyle := style.NewStyle().Foreground(sr.theme.Success)
			labelStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)

			fmt.Fprintf(sr.w, "%s %s\n\n", 
				iconStyle.Render("✔"),
				labelStyle.Render(fmt.Sprintf("thought (%.1fs)", elapsed)),
			)
			sr.lastEndedWithNewline = true
		}
	}
}

func (sr *StreamRenderer) Write(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()

	if len(chunk) > 0 {
		sr.hasWrittenText = true
		sr.lastEndedWithNewline = strings.HasSuffix(chunk, "\n")
	}

	for _, char := range chunk {
		if sr.inCodeBlock {
			if char == '\n' {
				line := sr.lineBuffer.String()
				sr.lineBuffer.Reset()

				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "```") {
					sr.inCodeBlock = false
				} else {
					if strings.HasPrefix(trimmed, "`") {
						fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Highlight).Render(line+"\n"))
					} else {
						fmt.Fprint(sr.w, "\n")
					}
				}
			} else {
				sr.lineBuffer.WriteRune(char)
				// Suppress real-time rendering if the line starts with a backtick (e.g. closing tag)
				if !strings.HasPrefix(strings.TrimSpace(sr.lineBuffer.String()), "`") {
					fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Highlight).Render(string(char)))
				}
			}
		} else {
			if char == '\n' {
				line := sr.lineBuffer.String()
				sr.lineBuffer.Reset()

				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "```") {
					ppw := findPromptPreservingWriter(sr.w)
					if ppw != nil && sr.hasLineStart {
						scrollDelta := ppw.GetScrollCount() - sr.lineStartScroll
						adjustedLine := sr.lineStartLine - scrollDelta
						if adjustedLine < 1 {
							adjustedLine = 1
						}
						clearScrollRegionLines(ppw, adjustedLine, sr.w)
						ppw.SetPrintLine(adjustedLine)
						ppw.SetPrintCol(sr.lineStartCol)
					}
					sr.inCodeBlock = true
					sr.codeLanguage = strings.TrimPrefix(trimmed, "```")
					if sr.codeLanguage == "" {
						sr.codeLanguage = "plaintext"
					}
				} else {
					ppw := findPromptPreservingWriter(sr.w)
					if ppw != nil && sr.hasLineStart {
						scrollDelta := ppw.GetScrollCount() - sr.lineStartScroll
						adjustedLine := sr.lineStartLine - scrollDelta
						if adjustedLine < 1 {
							adjustedLine = 1
						}
						clearScrollRegionLines(ppw, adjustedLine, sr.w)
						ppw.SetPrintLine(adjustedLine)
						ppw.SetPrintCol(sr.lineStartCol)
						sr.printNormalLine(line)
						fmt.Fprint(sr.w, "\n")
					} else {
						sr.printNormalLine(line)
						fmt.Fprint(sr.w, "\n")
					}
				}
				sr.hasLineStart = false
			} else {
				ppw := findPromptPreservingWriter(sr.w)
				if ppw != nil {
					if !sr.hasLineStart {
						sr.lineStartLine = ppw.GetPrintLine()
						sr.lineStartCol = ppw.GetPrintCol()
						sr.lineStartScroll = ppw.GetScrollCount()
						sr.hasLineStart = true
					}
					fmt.Fprint(sr.w, string(char))
				}
				sr.lineBuffer.WriteRune(char)
			}
		}
	}
}

func (sr *StreamRenderer) Flush() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.flushLocked()
}

func (sr *StreamRenderer) flushLocked() {
	sr.endThinking()

	if sr.parser != nil && sr.parser.activeToolName != "" {
		if !sr.parser.titlePrinted {
			sr.parser.printStreamTitle(sr.w, sr.theme)
		}
		if sr.parser.outputBuf.Len() > 0 {
			fmt.Fprint(sr.w, sr.parser.outputBuf.String())
			sr.parser.outputBuf.Reset()
		}
	}

	if sr.inCodeBlock {
		rem := sr.lineBuffer.String()
		sr.lineBuffer.Reset()
		if rem != "" {
			if !strings.HasPrefix(strings.TrimSpace(rem), "```") {
				fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Highlight).Render(rem+"\n"))
			}
		}
		sr.inCodeBlock = false
		sr.lastEndedWithNewline = true
		return
	}

	rem := sr.lineBuffer.String()
	if rem != "" {
		ppw := findPromptPreservingWriter(sr.w)
		if ppw != nil && sr.hasLineStart {
			scrollDelta := ppw.GetScrollCount() - sr.lineStartScroll
			adjustedLine := sr.lineStartLine - scrollDelta
			if adjustedLine < 1 {
				adjustedLine = 1
			}
			clearScrollRegionLines(ppw, adjustedLine, sr.w)
			ppw.SetPrintLine(adjustedLine)
			ppw.SetPrintCol(sr.lineStartCol)
			sr.printNormalLine(rem)
		} else {
			sr.printNormalLine(rem)
		}
		sr.lineBuffer.Reset()
		sr.lastEndedWithNewline = strings.HasSuffix(rem, "\n")
	}
	sr.hasLineStart = false

	if sr.hasWrittenText && !sr.lastEndedWithNewline {
		fmt.Fprint(sr.w, "\n")
		sr.lastEndedWithNewline = true
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
				styled := style.NewStyle().Foreground(sr.theme.Highlight).Render(codeVal)
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
				styled := style.NewStyle().Foreground(sr.theme.Primary).Bold(true).Render(sr.renderInlineMarkdown(boldVal))
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
				styled := style.NewStyle().Italic(true).Render(sr.renderInlineMarkdown(italicVal))
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
	// No-op: suppress streaming tool calls to avoid double rendering and scrollback pollution.
	// Tool calls are printed cleanly and exactly once during the execution phase.
}

func (sr *StreamRenderer) WriteToolCall(content string) {
	// No-op: suppress streaming tool calls to avoid double rendering and scrollback pollution.
}

func (sr *StreamRenderer) GetToolTitleLineNumber(index int) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.toolTitleLineNumbers) {
		return -1
	}
	return sr.parser.toolTitleLineNumbers[index]
}

func (sr *StreamRenderer) ShiftToolTitleLineNumbers(startIdx int, diff int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser != nil {
		for i := startIdx; i < len(sr.parser.toolTitleLineNumbers); i++ {
			if i >= 0 && i < len(sr.parser.toolTitleLineNumbers) {
				sr.parser.toolTitleLineNumbers[i] += diff
			}
		}
	}
}

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}
