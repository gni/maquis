package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"maquis/pkg/ui/style"
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
								if len(ext) > 1 { p.guessedLang = ext[1:] } else { p.guessedLang = "plaintext" }
							}
							lang := p.guessedLang
							if lang == "" { lang = "plaintext" }
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							fmt.Fprint(pw, "\n")
							p.lineBuffer.Reset()
						} else {
							p.lineBuffer.WriteString(unescaped)
						}
					} else if p.isPath {
						p.path += unescaped
						fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(unescaped))
					} else if p.isOldText {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Error).Render(unescaped))
						}
					} else if p.isNewText {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Success).Render(unescaped))
						}
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
						fmt.Fprintln(pw)
					}
					if p.isContent {
						if p.lineBuffer.Len() > 0 {
							lang := p.guessedLang
							if lang == "" { lang = "plaintext" }
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							p.lineBuffer.Reset()
						}
					}
					if p.isContent || p.isOldText || p.isNewText {
						fmt.Fprintln(pw)
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
							if p.guessedLang == "" && p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 { p.guessedLang = ext[1:] } else { p.guessedLang = "plaintext" }
							}
							lang := p.guessedLang
							if lang == "" { lang = "plaintext" }
							_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
							fmt.Fprint(pw, "\n")
							p.lineBuffer.Reset()
						} else {
							p.lineBuffer.WriteString(charStr)
						}
					} else if p.isPath {
						p.path += charStr
						fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(charStr))
					} else if p.isOldText {
						if charStr == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Error).Render(charStr))
						}
					} else if p.isNewText {
						if charStr == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Success).Render(charStr))
						}
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
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
					fmt.Fprintf(pw, "  ▸ %s: ", p.currentKey)
				} else if p.currentKey == "path" || (p.currentKey == "command" && p.activeToolName == "ls") || p.currentKey == "name" || p.currentKey == "id" || p.currentKey == "prompt" {
					p.isPath = true
					fmt.Fprintf(pw, "  ▸ %s: ", p.currentKey)
				} else if p.currentKey == "oldText" {
					if p.streamWrites {
						p.isOldText = true
						if !p.titlePrinted {
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						}
						fmt.Fprintf(pw, "  ▸ %s:\n", p.currentKey)
					} else {
						p.isOldText = true
					}
				} else if p.currentKey == "newText" {
					if p.streamWrites {
						p.isNewText = true
						p.guessedLang = ""
						if !p.titlePrinted {
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						}
						fmt.Fprintf(pw, "  ▸ %s:\n", p.currentKey)
					} else {
						p.isNewText = true
					}
				} else if p.currentKey == "write_content" || p.currentKey == "content" || strings.Contains(p.currentKey, "Content") {
					if p.streamWrites {
						p.isContent = true
						p.guessedLang = ""
						if !p.titlePrinted {
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						}
						fmt.Fprintf(pw, "  ▸ %s:\n", p.currentKey)
					} else {
						p.isContent = true
					}
				}
			} else if char == '}' || char == ']' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					lang := p.guessedLang
					if lang == "" { lang = "plaintext" }
					_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
					fmt.Fprint(pw, "\n")
					p.lineBuffer.Reset()
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
				p.isOldText = false
				p.isNewText = false
			} else if char == ',' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					lang := p.guessedLang
					if lang == "" { lang = "plaintext" }
					_ = HighlightWithoutTrailingNewline(pw, p.lineBuffer.String(), lang, theme.ChromaStyle)
					fmt.Fprint(pw, "\n")
					p.lineBuffer.Reset()
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
				p.isOldText = false
				p.isNewText = false
			}
		}
	}
}

func (p *jsonStreamParser) printStreamTitle(w io.Writer, theme UITheme) {
	p.titlePrinted = true
}

func (p *jsonStreamParser) updateStreamTitleWithPath(w io.Writer, theme UITheme) {
	// Disabled: Relative downward/upward cursor jumps (\x1b[%dB) corrupt
	// absolute positioning if the terminal wrapped or scrolled off-screen.
	// If the LLM sends 'path' late, the title will just remain generically generic.
}
