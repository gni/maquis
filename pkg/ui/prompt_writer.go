package ui

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)


// PromptPreservingWriter wraps an io.Writer (typically os.Stderr) and ensures
// that all streamed output is confined to the terminal's scrolling region
// (rows 1 through height-5). After every write, the cursor is repositioned
// back to the prompt input line (height-2, column 3) so it blinks stably
// at the "> " prompt — even while tokens are streaming above it.
//
// The bottom 6 lines of the terminal are reserved:
//   - height-5: blank line space
//   - height-4: stats line content
//   - height-3: ─── prompt ──────────────────────────────[reasoning:low]
//   - height-2: > (input line, cursor blinks here)
//   - height-1: ────────────────────────────────────────── (status separator)
//   - height:   status bar content
type PromptPreservingWriter struct {
	inner          io.Writer
	height         int
	printLine      int // current row inside the scroll region
	printCol       int // current column on printLine (1-based)
	promptCol      int // current column on promptLine for cursor restore (1-based)
	ansiState      int // 0: normal, 1: saw ESC, 2: saw ESC [, 3: saw ESC ]
	cursorHidden   bool
	cursorAtPrompt bool
	scrollCount    int
}

func NewPromptPreservingWriter(w io.Writer, height int) *PromptPreservingWriter {
	scrollBottom := height - 6
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	return &PromptPreservingWriter{
		inner:          w,
		height:         height,
		printLine:      scrollBottom,
		printCol:       1,
		promptCol:      3, // default to 3 (after "> ")
		ansiState:      0,
		cursorHidden:   true, // Default to true because the cursor is hidden during agent execution
		cursorAtPrompt: true,
	}
}

func (p *PromptPreservingWriter) SetCursorHidden(hidden bool) {
	TerminalMu.Lock()
	p.cursorHidden = hidden
	TerminalMu.Unlock()
}

func (p *PromptPreservingWriter) SetPromptCol(col int) {
	TerminalMu.Lock()
	p.promptCol = col
	TerminalMu.Unlock()
}

func (p *PromptPreservingWriter) ForceReposition() {
	TerminalMu.Lock()
	p.cursorAtPrompt = true
	scrollBottom := p.height - 6
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	p.printLine = scrollBottom
	p.printCol = 1
	TerminalMu.Unlock()
}

func (p *PromptPreservingWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	// Update height dynamically to handle terminal resizes robustly
	if h := getRealTerminalHeight(); h > 0 {
		if h != p.height {
			diff := h - p.height
			p.height = h
			p.printLine += diff
			scrollBottom := h - 6
			if scrollBottom < 1 {
				scrollBottom = 1
			}
			if p.printLine > scrollBottom {
				p.printLine = scrollBottom
			}
			if p.printLine < 1 {
				p.printLine = 1
			}
		}
	}

	// Always position cursor to the tracked print position to prevent drift or leak into other rows
	fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.printLine, p.printCol)
	p.cursorAtPrompt = false

	// Write the actual content. The terminal's scrolling region (set to 1..height-6)
	// ensures that newlines here only scroll within that region.
	n, err := p.inner.Write(data)

	// Track the column position so we can restore it accurately next time.
	p.trackPosition(data[:n])

	// Restore cursor to the prompt input line (height-2, promptCol)
	if !p.cursorHidden {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.height-2, p.promptCol)
		p.cursorAtPrompt = true
	}

	return n, err
}

// trackPosition updates printCol by analyzing the written bytes.
// On newline, printCol resets to 1 (the terminal scrolls within the region).
// On carriage return, printCol resets to 1 without scrolling.
func (p *PromptPreservingWriter) trackPosition(data []byte) {
	s := string(data)
	scrollBottom := p.height - 6
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	for _, r := range s {
		switch p.ansiState {
		case 0: // normal
			if r == '\x1b' {
				p.ansiState = 1
			} else {
				switch r {
				case '\n':
					p.printCol = 1
					if p.printLine < scrollBottom {
						p.printLine++
					} else {
						p.scrollCount++
					}
				case '\r':
					p.printCol = 1
				case '\b':
					if p.printCol > 1 {
						p.printCol--
					}
				case '\t':
					p.printCol = ((p.printCol - 1) / 8 * 8) + 8 + 1
				default:
					if r >= 32 && r != '\uFFFD' {
						p.printCol++
					}
				}
			}
		case 1: // saw ESC
			if r == '[' {
				p.ansiState = 2
			} else if r == ']' {
				p.ansiState = 3
			} else {
				p.ansiState = 0
			}
		case 2: // saw ESC [ (CSI)
			if r >= 0x40 && r <= 0x7E {
				p.ansiState = 0
			}
		case 3: // saw ESC ] (OSC)
			if r == '\x07' || r == '\\' {
				p.ansiState = 0
			}
		}
	}

	// Clamp printCol based on terminal width to handle auto-wrap correctly
	width := getRealTerminalWidth()
	if width <= 0 {
		width = 80
	}
	if p.printCol > width {
		p.printCol = ((p.printCol - 1) % width) + 1
		if p.printLine < scrollBottom {
			p.printLine++
		} else {
			p.scrollCount++
		}
	}
}

func (p *PromptPreservingWriter) Height() int {
	return p.height
}

func (p *PromptPreservingWriter) Unwrap() io.Writer {
	return p.inner
}

func (p *PromptPreservingWriter) GetPrintLine() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.printLine
}

func (p *PromptPreservingWriter) GetPrintCol() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.printCol
}

func (p *PromptPreservingWriter) GetScrollCount() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.scrollCount
}

func (p *PromptPreservingWriter) SetPrintLine(line int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.printLine = line
}

func (p *PromptPreservingWriter) SetPrintCol(col int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.printCol = col
}

func getRealTerminalHeight() int {
	if _, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && h > 0 {
		return h
	}
	if _, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return h
	}
	if _, h, err := term.GetSize(int(os.Stderr.Fd())); err == nil && h > 0 {
		return h
	}
	return 0
}

func getRealTerminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 {
		return w
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}


