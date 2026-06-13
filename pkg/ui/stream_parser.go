package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bidouille/pkg/ui/style"
)

type jsonStreamParser struct {
	inString    bool
	inEscape    bool
	currentKey  string
	inValue     bool
	buf         strings.Builder
	isContent   bool
	isPath      bool
	pathPrinted bool

	// Fields for syntax highlighting streamed code
	path       string
	lineBuffer strings.Builder

	// Fields for diff streaming in edit tool
	isOldText bool
	isNewText bool

	// Field for language auto-detection
	guessedLang string

	// Fields for path-in-title rendering
	activeToolName string
	titlePrinted   bool

	// Output buffer for lazy rendering when path is not yet known
	outputBuf strings.Builder

	needsLeadingNewline  bool
	toolTitleLineNumbers []int
	activeToolIndex      int
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit" || p.activeToolName == "ls" || p.activeToolName == "grep" || p.activeToolName == "find"
}

type parserWriter struct {
	p *jsonStreamParser
	w io.Writer
}

func (pw parserWriter) Write(data []byte) (int, error) {
	if pw.p.needsPath() && pw.p.path == "" && !pw.p.titlePrinted {
		return pw.p.outputBuf.Write(data)
	}
	return pw.w.Write(data)
}

func (pw parserWriter) Unwrap() io.Writer {
	return pw.w
}

func (p *jsonStreamParser) feed(chunk string, w io.Writer, theme UITheme) {
	pw := parserWriter{p: p, w: w}

	for i := 0; i < len(chunk); i++ {
		char := chunk[i]

		if p.inString {
			if p.inEscape {
				p.inEscape = false
				var unescaped string
				switch char {
				case 'n':
					unescaped = "\n"
				case 't':
					unescaped = "\t"
				case 'r':
					unescaped = "\r"
				case '"', '\\', '/':
					unescaped = string(char)
				default:
					unescaped = "\\" + string(char)
				}

				if p.inValue {
					if p.isContent {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(unescaped))
						}
					} else if p.isPath {
						p.path += unescaped
					} else if p.isOldText {
						// Suppress raw oldText from stream output
					} else if p.isNewText {
						// Suppress raw newText from stream output
					}
				} else {
					p.buf.WriteString(unescaped)
				}
			} else if char == '\\' {
				p.inEscape = true
			} else if char == '"' {
				p.inString = false
				strVal := p.buf.String()
				p.buf.Reset()

				if !p.inValue {
					p.currentKey = strVal
				} else {
					if p.isPath {
						if !p.titlePrinted {
							p.pathPrinted = true
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						} else if !p.pathPrinted {
							p.pathPrinted = true
							p.updateStreamTitleWithPath(w, theme)
						}
					}
					p.inValue = false
					p.isContent = false
					p.isPath = false
					p.isOldText = false
					p.isNewText = false
				}
			} else {
				if p.inValue {
					charStr := string(char)
					if p.isContent {
						if char == '\n' {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(charStr))
						}
					} else if p.isPath {
						p.path += charStr
					} else if p.isOldText {
						// Suppress raw oldText from stream output
					} else if p.isNewText {
						// Suppress raw newText from stream output
					}
				} else {
					p.buf.WriteByte(char)
				}
			}
		} else {
			if char == '"' {
				p.inString = true
			} else if char == ':' {
				p.inValue = true
				isContentKey := p.currentKey == "content" || p.currentKey == "write_content" || p.currentKey == "command"
				if isContentKey && p.activeToolName != "write" {
					p.isContent = true
					p.guessedLang = ""
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
				} else if p.currentKey == "path" {
					p.isPath = true
				} else if p.currentKey == "oldText" {
					p.isOldText = true
				} else if p.currentKey == "newText" {
					p.isNewText = true
				}
			} else if char == '}' || char == ']' {
				p.inValue = false
				p.isContent = false
				p.isPath = false
				p.isOldText = false
				p.isNewText = false
			} else if char == ',' {
				p.inValue = false
				p.isContent = false
				p.isPath = false
				p.isOldText = false
				p.isNewText = false
			}
		}
	}
}

func guessLanguage(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") || strings.HasPrefix(trimmed, "def ") {
		return "python"
	}
	if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "func ") {
		return "go"
	}
	if strings.HasPrefix(trimmed, "#include") || strings.HasPrefix(trimmed, "using namespace") {
		return "cpp"
	}
	if strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "public ") || strings.HasPrefix(trimmed, "private ") {
		return "java"
	}
	if strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "let ") || strings.HasPrefix(trimmed, "function ") {
		return "javascript"
	}
	if strings.HasPrefix(trimmed, "<?php") {
		return "php"
	}
	if strings.HasPrefix(trimmed, "<!") || strings.HasPrefix(trimmed, "<html") {
		return "html"
	}
	return ""
}

func (p *jsonStreamParser) printTitle(w io.Writer, title string) {
	if p.needsLeadingNewline {
		fmt.Fprintln(w)
		p.needsLeadingNewline = false
	}
	fmt.Fprint(w, title+"\n")
}

func (p *jsonStreamParser) printStreamTitle(w io.Writer, theme UITheme) {
	if p.titlePrinted {
		return
	}
	p.titlePrinted = true

	startCount := getNewlineCount(w)
	if p.needsLeadingNewline {
		startCount++
	}
	p.toolTitleLineNumbers = append(p.toolTitleLineNumbers, startCount)

	var dotStr string
	if p.activeToolName == "write" {
		dotStr = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◇")
	} else {
		dotStyle := style.NewStyle().Foreground(style.Color("#fbbf24")).Bold(true)
		dotStr = dotStyle.Render("▸")
	}

	var title string
	if p.needsPath() && p.path != "" {
		title = FormatToolTitle(dotStr, p.activeToolName, p.path, theme)
	} else {
		title = FormatToolTitle(dotStr, p.activeToolName, "", theme)
	}
	p.printTitle(w, title)
}

func (p *jsonStreamParser) updateStreamTitleWithPath(w io.Writer, theme UITheme) {
	if len(p.toolTitleLineNumbers) == 0 {
		return
	}
	titleLine := p.toolTitleLineNumbers[len(p.toolTitleLineNumbers)-1]
	currentCount := getNewlineCount(w)
	diff := currentCount - titleLine

	_, height := getTerminalSize()
	if diff >= 0 && diff < height-1 {
		var dotStr string
		if p.activeToolName == "write" {
			dotStr = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◇")
		} else {
			dotStyle := style.NewStyle().Foreground(style.Color("#fbbf24")).Bold(true)
			dotStr = dotStyle.Render("▸")
		}

		title := FormatToolTitle(dotStr, p.activeToolName, p.path, theme)
		fmt.Fprintf(w, "\x1b[%dA\r\x1b[K%s\x1b[%dB\r", diff, title, diff)
	}
}

func getRelativePath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return path
	}
	return rel
}

func getNewlineCount(w io.Writer) int {
	if n, ok := w.(*newlineCounterWriter); ok {
		return n.GetCount()
	}
	return 0
}

type newlineCounterWriter struct {
	io.Writer
	count int
}

func (n *newlineCounterWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			n.count++
		}
	}
	return n.Writer.Write(p)
}

func (n *newlineCounterWriter) GetCount() int {
	return n.count
}
