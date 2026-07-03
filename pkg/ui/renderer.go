package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
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

	reasoningLineBuffer      strings.Builder
	reasoningLineStartLine   int
	reasoningLineStartCol    int
	reasoningLineStartScroll int
	hasReasoningLineStart    bool
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
	sr := &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		showFullThinking:     true, // Thinking is always full/expanded
		lastEndedWithNewline: true,
		streamWrites:         streamWrites,
		agentName:            agentName,
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

	if !sr.showThinking {
		return
	}
	if len(chunk) > 0 {
		sr.checkFirstWrite()
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

	for _, char := range chunk {
		if char == '\n' {
			line := sr.reasoningLineBuffer.String()
			sr.reasoningLineBuffer.Reset()

			trimmed := strings.TrimSpace(line)
			if sr.inCodeBlock {
				if strings.HasPrefix(trimmed, "```") {
					sr.inCodeBlock = false
				} else {
					ppw := findPromptPreservingWriter(sr.w)
					if ppw != nil && sr.hasReasoningLineStart {
						scrollDelta := ppw.GetScrollCount() - sr.reasoningLineStartScroll
						adjustedLine := sr.reasoningLineStartLine - scrollDelta
						if adjustedLine < 1 {
							adjustedLine = 1
						}
						if ppw.GetPrintLine() == adjustedLine {
							clearScrollRegionLines(ppw, adjustedLine, sr.w)
							ppw.SetPrintLine(adjustedLine)
							ppw.SetPrintCol(sr.reasoningLineStartCol)
							fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Border).Italic(true).Render("  "+line))
							fmt.Fprint(sr.w, "\n")
						} else {
							fmt.Fprint(sr.w, "\n")
						}
					} else {
						fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Border).Italic(true).Render("  "+line))
						fmt.Fprint(sr.w, "\n")
					}
				}
			} else {
				if strings.HasPrefix(trimmed, "```") {
					ppw := findPromptPreservingWriter(sr.w)
					if ppw != nil && sr.hasReasoningLineStart {
						scrollDelta := ppw.GetScrollCount() - sr.reasoningLineStartScroll
						adjustedLine := sr.reasoningLineStartLine - scrollDelta
						if adjustedLine < 1 {
							adjustedLine = 1
						}
						if ppw.GetPrintLine() == adjustedLine {
							clearScrollRegionLines(ppw, adjustedLine, sr.w)
							ppw.SetPrintLine(adjustedLine)
							ppw.SetPrintCol(sr.reasoningLineStartCol)
						}
					}
					sr.inCodeBlock = true
				} else {
					ppw := findPromptPreservingWriter(sr.w)
					if ppw != nil && sr.hasReasoningLineStart {
						scrollDelta := ppw.GetScrollCount() - sr.reasoningLineStartScroll
						adjustedLine := sr.reasoningLineStartLine - scrollDelta
						if adjustedLine < 1 {
							adjustedLine = 1
						}
						if ppw.GetPrintLine() == adjustedLine {
							clearScrollRegionLines(ppw, adjustedLine, sr.w)
							ppw.SetPrintLine(adjustedLine)
							ppw.SetPrintCol(sr.reasoningLineStartCol)
							
							termW, _ := getTerminalSize()
							wrapLimit := termW - sr.reasoningLineStartCol - 1
							if wrapLimit < 20 {
								wrapLimit = 20
							}
							wrapped := wrapMarkdownLine(line, wrapLimit)
							for idx, wl := range wrapped {
								sr.printReasoningLine(wl)
								if idx < len(wrapped)-1 {
									fmt.Fprint(sr.w, "\n")
								}
							}
							fmt.Fprint(sr.w, "\n")
						} else {
							fmt.Fprint(sr.w, "\n")
						}
					} else {
						termW, _ := getTerminalSize()
						wrapLimit := termW - 5
						if wrapLimit < 20 {
							wrapLimit = 20
						}
						wrapped := wrapMarkdownLine(line, wrapLimit)
						for idx, wl := range wrapped {
							sr.printReasoningLine(wl)
							if idx < len(wrapped)-1 {
								fmt.Fprint(sr.w, "\n")
							}
						}
						fmt.Fprint(sr.w, "\n")
					}
				}
			}
			sr.hasReasoningLineStart = false
		} else {
			ppw := findPromptPreservingWriter(sr.w)
			if ppw != nil {
				if !sr.hasReasoningLineStart {
					sr.reasoningLineStartLine = ppw.GetPrintLine()
					sr.reasoningLineStartCol = ppw.GetPrintCol()
					sr.reasoningLineStartScroll = ppw.GetScrollCount()
					sr.hasReasoningLineStart = true
				}
			}
			if sr.inCodeBlock {
				fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Border).Italic(true).Render(string(char)))
			} else {
				fmt.Fprint(sr.w, string(char))
			}
			sr.reasoningLineBuffer.WriteRune(char)
		}
	}
}

func (sr *StreamRenderer) EndThinking() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()
}

func (sr *StreamRenderer) endThinking() {
	if sr.inThinking {
		sr.inThinking = false

		rem := sr.reasoningLineBuffer.String()
		if rem != "" {
			ppw := findPromptPreservingWriter(sr.w)
			if ppw != nil && sr.hasReasoningLineStart {
				scrollDelta := ppw.GetScrollCount() - sr.reasoningLineStartScroll
				adjustedLine := sr.reasoningLineStartLine - scrollDelta
				if adjustedLine < 1 {
					adjustedLine = 1
				}
				if ppw.GetPrintLine() == adjustedLine {
					clearScrollRegionLines(ppw, adjustedLine, sr.w)
					ppw.SetPrintLine(adjustedLine)
					ppw.SetPrintCol(sr.reasoningLineStartCol)
					
					termW, _ := getTerminalSize()
					wrapLimit := termW - sr.reasoningLineStartCol - 1
					if wrapLimit < 20 {
						wrapLimit = 20
					}
					wrapped := wrapMarkdownLine(rem, wrapLimit)
					for idx, wl := range wrapped {
						sr.printReasoningLine(wl)
						if idx < len(wrapped)-1 {
							fmt.Fprint(sr.w, "\n")
						}
					}
				}
			} else {
				termW, _ := getTerminalSize()
				wrapLimit := termW - 5
				if wrapLimit < 20 {
					wrapLimit = 20
				}
				wrapped := wrapMarkdownLine(rem, wrapLimit)
				for idx, wl := range wrapped {
					sr.printReasoningLine(wl)
					if idx < len(wrapped)-1 {
						fmt.Fprint(sr.w, "\n")
					}
				}
			}
			sr.reasoningLineBuffer.Reset()
		}
		sr.hasReasoningLineStart = false

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
		sr.checkFirstWrite()
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
						if ppw.GetPrintLine() == adjustedLine {
							clearScrollRegionLines(ppw, adjustedLine, sr.w)
							ppw.SetPrintLine(adjustedLine)
							ppw.SetPrintCol(sr.lineStartCol)
						}
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
						if ppw.GetPrintLine() == adjustedLine {
							clearScrollRegionLines(ppw, adjustedLine, sr.w)
							ppw.SetPrintLine(adjustedLine)
							ppw.SetPrintCol(sr.lineStartCol)
							
							termW, _ := getTerminalSize()
							wrapLimit := termW - sr.lineStartCol - 1
							if wrapLimit < 20 {
								wrapLimit = 20
							}
							wrapped := wrapMarkdownLine(line, wrapLimit)
							for idx, wl := range wrapped {
								sr.printNormalLine(wl)
								if idx < len(wrapped)-1 {
									fmt.Fprint(sr.w, "\n")
								}
							}
							fmt.Fprint(sr.w, "\n")
						} else {
							fmt.Fprint(sr.w, "\n")
						}
					} else {
						termW, _ := getTerminalSize()
						wrapLimit := termW - 5
						if wrapLimit < 20 {
							wrapLimit = 20
						}
						wrapped := wrapMarkdownLine(line, wrapLimit)
						for idx, wl := range wrapped {
							sr.printNormalLine(wl)
							if idx < len(wrapped)-1 {
								fmt.Fprint(sr.w, "\n")
							}
						}
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
				}
				fmt.Fprint(sr.w, string(char))
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
		if sr.parser.lineBuffer.Len() > 0 {
			lang := sr.parser.guessedLang
			if lang == "" { lang = "plaintext" }
			_ = HighlightWithoutTrailingNewline(sr.w, sr.parser.lineBuffer.String(), lang, sr.theme.ChromaStyle)
			fmt.Fprint(sr.w, "\n")
			sr.parser.lineBuffer.Reset()
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
			if ppw.GetPrintLine() == adjustedLine {
				clearScrollRegionLines(ppw, adjustedLine, sr.w)
				ppw.SetPrintLine(adjustedLine)
				ppw.SetPrintCol(sr.lineStartCol)
				
				termW, _ := getTerminalSize()
				wrapLimit := termW - sr.lineStartCol - 1
				if wrapLimit < 20 {
					wrapLimit = 20
				}
				wrapped := wrapMarkdownLine(rem, wrapLimit)
				for idx, wl := range wrapped {
					sr.printNormalLine(wl)
					if idx < len(wrapped)-1 {
						fmt.Fprint(sr.w, "\n")
					}
				}
			}
		} else {
			termW, _ := getTerminalSize()
			wrapLimit := termW - 5
			if wrapLimit < 20 {
				wrapLimit = 20
			}
			wrapped := wrapMarkdownLine(rem, wrapLimit)
			for idx, wl := range wrapped {
				sr.printNormalLine(wl)
				if idx < len(wrapped)-1 {
					fmt.Fprint(sr.w, "\n")
				}
			}
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

	if sr.parser != nil {
		sr.parser.activeToolName = toolName
		sr.parser.activeToolIndex = toolCallIndex
		sr.parser.titlePrinted = false
		sr.parser.path = ""
		sr.parser.pathPrinted = false
		sr.parser.isContent = false
		sr.parser.isPath = false
		sr.parser.isOldText = false
		sr.parser.isNewText = false
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
