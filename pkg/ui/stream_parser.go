package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
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

	// Field for language auto-detection
	guessedLang string

	// Fields for path-in-title rendering
	activeToolName string
	titlePrinted   bool

	// Output buffer for lazy rendering when path is not yet known
	outputBuf strings.Builder

	needsLeadingNewline  bool
	toolTitleLineNumbers []int
	toolBodyStreamed     []bool
	activeToolIndex      int
	streamWrites         bool
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit" || p.activeToolName == "ls" || p.activeToolName == "spawn_subagent" || p.activeToolName == "load_skill" || p.activeToolName == "task_status" || p.activeToolName == "task_kill" || strings.HasPrefix(p.activeToolName, "subagent__")
}

type parserWriter struct {
	p *jsonStreamParser
	w io.Writer
}

func (pw parserWriter) Write(data []byte) (int, error) {
	if pw.p.needsPath() && pw.p.path == "" && !pw.p.titlePrinted && !pw.p.isPath {
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
							if p.guessedLang == "" && p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									p.guessedLang = ext[1:]
								} else {
									p.guessedLang = "plaintext"
								}
							}
							lang := p.guessedLang
							if lang == "" {
								lang = "plaintext"
							}
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							fmt.Fprint(pw, "\n")
							p.lineBuffer.Reset()
						} else {
							p.lineBuffer.WriteString(unescaped)
						}
					} else if p.isPath {
						p.path += unescaped
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
					if p.isContent {
						if p.lineBuffer.Len() > 0 {
							lang := p.guessedLang
							if lang == "" {
								lang = "plaintext"
							}
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							p.lineBuffer.Reset()
						}
					}
					if p.isContent {
						fmt.Fprintln(pw)
					}
					p.inValue = false
					p.isContent = false
					p.isPath = false
				}
			} else {
				if p.inValue {
					charStr := string(char)
					if p.isContent {
						if char == '\n' {
							if p.guessedLang == "" && p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									p.guessedLang = ext[1:]
								} else {
									p.guessedLang = "plaintext"
								}
							}
							lang := p.guessedLang
							if lang == "" {
								lang = "plaintext"
							}
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							fmt.Fprint(pw, "\n")
							p.lineBuffer.Reset()
						} else {
							p.lineBuffer.WriteString(charStr)
						}
					} else if p.isPath {
						p.path += charStr
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
				isContentKey := (p.currentKey == "command" && p.activeToolName != "ls")
				if isContentKey {
					p.isContent = true
					p.guessedLang = ""
					p.markBodyStreamed()
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
					fmt.Fprintf(pw, "▸ %s: ", p.currentKey)
				} else if p.currentKey == "path" || (p.currentKey == "command" && p.activeToolName == "ls") || p.currentKey == "name" || p.currentKey == "id" || p.currentKey == "prompt" {
					p.isPath = true
				} else if p.currentKey == "write_content" || p.currentKey == "content" || strings.Contains(p.currentKey, "Content") {
					if p.streamWrites {
						p.isContent = true
						p.guessedLang = ""
						p.markBodyStreamed()
						if !p.titlePrinted {
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						}
					}
				}
			} else if char == '}' || char == ']' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					lang := p.guessedLang
					if lang == "" {
						lang = "plaintext"
					}
					_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
					fmt.Fprint(pw, "\n")
					p.lineBuffer.Reset()
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
			} else if char == ',' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					lang := p.guessedLang
					if lang == "" {
						lang = "plaintext"
					}
					_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
					fmt.Fprint(pw, "\n")
					p.lineBuffer.Reset()
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
			}
		}
	}
}

func (p *jsonStreamParser) printStreamTitle(w io.Writer, theme UITheme) {
	if p.titlePrinted {
		return
	}
	p.titlePrinted = true
	p.ensureTrackingIndex()
	p.toolTitleLineNumbers[p.activeToolIndex] = getNewlineCount(w)

	symbol := renderToolSymbol(p.activeToolName, toolStatusPending, theme)
	fmt.Fprintln(w, FormatToolTitle(symbol, p.activeToolName, p.path, theme))
}

func (p *jsonStreamParser) updateStreamTitleWithPath(w io.Writer, theme UITheme) {
	p.ensureTrackingIndex()
	line := p.toolTitleLineNumbers[p.activeToolIndex]
	symbol := renderToolSymbol(p.activeToolName, toolStatusPending, theme)
	replaceTrackedStreamLine(w, line, FormatToolTitle(symbol, p.activeToolName, p.path, theme))
}

func (p *jsonStreamParser) ensureTrackingIndex() {
	for len(p.toolTitleLineNumbers) <= p.activeToolIndex {
		p.toolTitleLineNumbers = append(p.toolTitleLineNumbers, -1)
	}
	for len(p.toolBodyStreamed) <= p.activeToolIndex {
		p.toolBodyStreamed = append(p.toolBodyStreamed, false)
	}
}

func (p *jsonStreamParser) markBodyStreamed() {
	p.ensureTrackingIndex()
	p.toolBodyStreamed[p.activeToolIndex] = true
}

func getNewlineCount(w io.Writer) int {
	for w != nil {
		if counter, ok := w.(interface{ GetCount() int }); ok {
			return counter.GetCount()
		}
		unwrapper, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			break
		}
		w = unwrapper.Unwrap()
	}
	return -1
}

func replaceTrackedStreamLine(w io.Writer, line int, content string) bool {
	if line < 0 {
		return false
	}
	currentLine := getNewlineCount(w)
	if currentLine < line {
		return false
	}
	writer := findPromptPreservingWriter(w)
	if writer == nil {
		return false
	}
	return writer.ReplaceScrollLineBack(currentLine-line, content)
}
