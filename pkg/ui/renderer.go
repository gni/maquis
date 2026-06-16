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
	reasoningStart   time.Time
	reasoningText    strings.Builder
	spinnerFrame     int

	// Streaming states for real-time text formatting
	lineIsHeader bool
	inBold       bool
	inInlineCode bool

	lastEndedWithNewline bool
	hasWrittenText       bool
	hasWrittenThoughts   bool
	parser               *jsonStreamParser
	streamWrites         bool
}

func NewStreamRenderer(w io.Writer, theme UITheme, showThinking bool, streamWrites bool) *StreamRenderer {
	return &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		showFullThinking:     true, // Thinking is always full/expanded
		lastEndedWithNewline: true,
		streamWrites:         streamWrites,
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
	}

	if len(chunk) > 0 {
		sr.hasWrittenThoughts = true
	}

	sr.reasoningText.WriteString(chunk)

	dimStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)
	fmt.Fprint(sr.w, dimStyle.Render(chunk))
}

func (sr *StreamRenderer) EndThinking() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()
}

func (sr *StreamRenderer) endThinking() {
	if sr.inThinking {
		sr.inThinking = false
		fmt.Fprint(sr.w, "\n")

		elapsed := time.Since(sr.reasoningStart).Seconds()
		iconStyle := style.NewStyle().Foreground(sr.theme.Success)
		labelStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)

		fmt.Fprintf(sr.w, "%s %s\n\n", 
			iconStyle.Render("✔"),
			labelStyle.Render(fmt.Sprintf("thought (%.1fs)", elapsed)),
		)
		sr.lastEndedWithNewline = true
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
					sr.inCodeBlock = true
					sr.codeLanguage = strings.TrimPrefix(trimmed, "```")
					if sr.codeLanguage == "" {
						sr.codeLanguage = "plaintext"
					}
				} else {
					if strings.HasPrefix(strings.TrimSpace(line), "`") {
						sr.printNormalLine(line + "\n")
					} else {
						fmt.Fprint(sr.w, "\n")
					}
				}

				sr.lineIsHeader = false
				sr.inBold = false
				sr.inInlineCode = false
			} else {
				currentLine := sr.lineBuffer.String()
				sr.lineBuffer.WriteRune(char)

				suppressPrint := strings.HasPrefix(strings.TrimSpace(currentLine+string(char)), "`")

				if !suppressPrint {
					if char == '`' {
						sr.inInlineCode = !sr.inInlineCode
					}

					if char == '*' && len(currentLine) > 0 && strings.HasSuffix(currentLine, "*") {
						sr.inBold = !sr.inBold
					}

					isHeaderPrefix := true
					bufferedStr := sr.lineBuffer.String()
					for _, r := range bufferedStr {
						if r != '#' && r != ' ' && r != '\t' {
							isHeaderPrefix = false
							break
						}
					}
					if isHeaderPrefix && strings.Contains(bufferedStr, "#") {
						sr.lineIsHeader = true
					}

					charStr := string(char)
					if sr.lineIsHeader {
						fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Secondary).Bold(true).Render(charStr))
					} else if sr.inInlineCode {
						fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Highlight).Render(charStr))
					} else if sr.inBold {
						fmt.Fprint(sr.w, style.NewStyle().Foreground(sr.theme.Primary).Bold(true).Render(charStr))
					} else {
						fmt.Fprint(sr.w, charStr)
					}
				}
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
		if strings.HasPrefix(strings.TrimSpace(rem), "`") {
			sr.printNormalLine(rem)
		}
		sr.lineBuffer.Reset()
		sr.lastEndedWithNewline = strings.HasSuffix(rem, "\n")
	}

	if sr.hasWrittenText && !sr.lastEndedWithNewline {
		fmt.Fprint(sr.w, "\n")
		sr.lastEndedWithNewline = true
	}
}

func (sr *StreamRenderer) printNormalLine(line string) {
	styled := line
	if strings.HasPrefix(line, "#") {
		styled = style.NewStyle().Foreground(sr.theme.Secondary).Bold(true).Render(line)
	} else {
		words := strings.Split(line, " ")
		for i, w := range words {
			if strings.HasPrefix(w, "**") && strings.HasSuffix(w, "**") {
				words[i] = style.NewStyle().Foreground(sr.theme.Primary).Bold(true).Render(strings.Trim(w, "*"))
			} else if strings.HasPrefix(w, "`") && strings.HasSuffix(w, "`") {
				words[i] = style.NewStyle().Foreground(sr.theme.Highlight).Render(strings.Trim(w, "`"))
			}
		}
		styled = strings.Join(words, " ")
	}

	fmt.Fprint(sr.w, styled)
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

	if sr.parser == nil {
		sr.parser = &jsonStreamParser{activeToolIndex: -1, streamWrites: sr.streamWrites}
	}
	sr.parser.streamWrites = sr.streamWrites
	if sr.parser.activeToolIndex != toolCallIndex || sr.parser.activeToolName == "" {
		sr.flushLocked()
		sr.parser.needsLeadingNewline = sr.hasWrittenText
		sr.parser.activeToolName = toolName
		sr.parser.activeToolIndex = toolCallIndex
		sr.parser.titlePrinted = false
		sr.parser.path = ""
		sr.parser.pathPrinted = false
	} else {
		sr.parser.activeToolName = toolName
	}
}

func (sr *StreamRenderer) WriteToolCall(content string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil {
		sr.parser = &jsonStreamParser{activeToolIndex: -1, streamWrites: sr.streamWrites}
	}
	sr.parser.streamWrites = sr.streamWrites
	sr.parser.feed(content, sr.w, sr.theme)
}

func (sr *StreamRenderer) GetToolTitleLineNumber(index int) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.toolTitleLineNumbers) {
		return -1
	}
	return sr.parser.toolTitleLineNumbers[index]
}
