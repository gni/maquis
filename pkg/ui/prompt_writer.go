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
// The bottom 5 lines of the terminal are reserved:
//   - height-4: stats line content
//   - height-3: ─── prompt ──────────────────────────────
//   - height-2: > (input line, cursor blinks here)
//   - height-1: ────────────────────────────────────────── (status separator)
//   - height:   status bar content
type PromptPreservingWriter struct {
	inner                 io.Writer
	height                int
	printLine             int // current row inside the scroll region
	printCol              int // current column on printLine (1-based)
	promptCol             int // current column on promptLine for cursor restore (1-based)
	ansiState             int // 0: normal, 1: saw ESC, 2: saw ESC [, 3: saw ESC ]
	cursorHidden          bool
	cursorAtPrompt        bool
	needsReposition       bool
	scrollCount           int
	restoreCursorToPrompt bool
	autoWrapPending       bool
	autoWrapDetached      bool
}

func (p *PromptPreservingWriter) getScrollBottom() int {
	scrollBottom := p.height - 5 - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	return scrollBottom
}

func NewPromptPreservingWriter(w io.Writer, height int) *PromptPreservingWriter {
	p := &PromptPreservingWriter{
		inner:                 w,
		height:                height,
		printCol:              1,
		promptCol:             3, // default to 3 (after "> ")
		ansiState:             0,
		cursorHidden:          false, // Default to false to keep cursor visible at prompt line
		cursorAtPrompt:        true,
		needsReposition:       true,
		restoreCursorToPrompt: true,
	}
	p.printLine = p.getScrollBottom()
	return p
}

func (p *PromptPreservingWriter) getPromptRow() int {
	row := p.height - 2 - getUI().PasteLinesOffset
	if row < 1 {
		row = 1
	}
	return row
}

func (p *PromptPreservingWriter) SetCursorHidden(hidden bool) {
	TerminalMu.Lock()
	p.cursorHidden = hidden
	if hidden {
		fmt.Fprintf(p.inner, "\x1b[?25l")
	} else {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH\x1b[?25h", p.getPromptRow(), p.promptCol)
		p.cursorAtPrompt = true
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	}
	TerminalMu.Unlock()
}

func (p *PromptPreservingWriter) SetRestoreCursorToPrompt(restore bool) {
	TerminalMu.Lock()
	p.restoreCursorToPrompt = restore
	if restore {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH\x1b[?25h", p.getPromptRow(), p.promptCol)
		p.cursorAtPrompt = true
	} else {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH\x1b[?25h", p.printLine, p.printCol)
		p.cursorAtPrompt = false
	}
	if p.autoWrapPending {
		p.autoWrapDetached = true
	}
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
	p.printLine = p.getScrollBottom()
	p.printCol = 1
	p.autoWrapPending = false
	p.autoWrapDetached = false
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
			scrollBottom := p.getScrollBottom()
			if p.printLine > scrollBottom {
				p.printLine = scrollBottom
			}
			if p.printLine < 1 {
				p.printLine = 1
			}
			if diff != 0 {
				p.needsReposition = true
			}
		}
	}

	if p.restoreCursorToPrompt && (p.cursorAtPrompt || p.needsReposition) {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.printLine, p.printCol)
		p.cursorAtPrompt = false
		p.needsReposition = false
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	} else if !p.restoreCursorToPrompt && p.needsReposition {
		fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.printLine, p.printCol)
		p.needsReposition = false
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	}

	// A VT terminal keeps the cursor on the last column with an internal
	// auto-wrap-pending flag. Moving the cursor to the prompt cancels that
	// terminal flag. Materialize the delayed wrap before the next printable
	// chunk so it cannot overwrite column one of the completed row.
	if p.autoWrapPending && p.autoWrapDetached && firstTerminalActionIsGraphic(data, p.ansiState) {
		if _, err := fmt.Fprint(p.inner, "\r\n"); err != nil {
			return 0, err
		}
		p.advancePrintLine()
		p.printCol = 1
		p.autoWrapPending = false
		p.autoWrapDetached = false
	}

	// Write the actual content. The terminal's scrolling region (set to 1..height-6)
	// ensures that newlines here only scroll within that region.
	n, err := p.inner.Write(data)

	// Track the column position so we can restore it accurately next time.
	p.trackPosition(data[:n])

	// Restore cursor to the prompt input line (height-2-PasteLinesOffset, promptCol) or keep it at stream position
	if !p.cursorHidden {
		if p.restoreCursorToPrompt {
			fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.getPromptRow(), p.promptCol)
			p.cursorAtPrompt = true
		} else {
			fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.printLine, p.printCol)
			p.cursorAtPrompt = false
		}
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	}

	return n, err
}

// trackPosition updates printCol by analyzing the written bytes.
// On newline, printCol resets to 1 (the terminal scrolls within the region).
// On carriage return, printCol resets to 1 without scrolling.
func (p *PromptPreservingWriter) trackPosition(data []byte) {
	s := string(data)
	termW, _ := getTerminalSize()

	for _, r := range s {
		switch p.ansiState {
		case 0: // normal
			if r == '\x1b' {
				p.ansiState = 1
			} else {
				switch r {
				case '\n':
					p.printCol = 1
					p.autoWrapPending = false
					p.autoWrapDetached = false
					p.advancePrintLine()
				case '\r':
					p.printCol = 1
					p.autoWrapPending = false
					p.autoWrapDetached = false
				case '\b':
					p.autoWrapPending = false
					p.autoWrapDetached = false
					if p.printCol > 1 {
						p.printCol--
					}
				case '\t':
					p.autoWrapPending = false
					p.autoWrapDetached = false
					nextCol := ((p.printCol-1)/8*8 + 8) + 1
					if termW > 0 && nextCol > termW {
						nextCol = termW
					}
					p.printCol = nextCol
				default:
					if r >= 32 && r != '\uFFFD' {
						if p.autoWrapPending {
							p.advancePrintLine()
							p.printCol = 1
							p.autoWrapPending = false
							p.autoWrapDetached = false
						}
						p.printCol++
						if termW > 0 && p.printCol > termW {
							p.printCol = termW
							p.autoWrapPending = true
							p.autoWrapDetached = false
						}
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
}

func (p *PromptPreservingWriter) advancePrintLine() {
	scrollBottom := p.getScrollBottom()
	if p.printLine < scrollBottom {
		p.printLine++
		return
	}
	p.scrollCount++
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
	p.needsReposition = true
	p.autoWrapPending = false
	p.autoWrapDetached = false
}

func (p *PromptPreservingWriter) SetPrintCol(col int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.printCol = col
	p.needsReposition = true
	p.autoWrapPending = false
	p.autoWrapDetached = false
}

func firstTerminalActionIsGraphic(data []byte, initialANSIState int) bool {
	state := initialANSIState
	for _, r := range string(data) {
		switch state {
		case 0:
			if r == '\x1b' {
				state = 1
				continue
			}
			switch r {
			case '\n', '\r', '\b':
				return false
			case '\t':
				return true
			default:
				if r >= 32 && r != '\uFFFD' {
					return true
				}
			}
		case 1:
			if r == '[' {
				state = 2
			} else if r == ']' {
				state = 3
			} else {
				state = 0
			}
		case 2:
			if r >= 0x40 && r <= 0x7E {
				state = 0
			}
		case 3:
			if r == '\a' || r == '\\' {
				state = 0
			}
		}
	}
	return false
}

// ReplaceScrollLineBack atomically replaces one visible line without changing
// the tracked stream position or repainting neighboring rows.
func (p *PromptPreservingWriter) ReplaceScrollLineBack(linesBack int, content string) bool {
	return p.ReplaceScrollBlockBack(linesBack, []string{content})
}

// ReplaceScrollBlockBack atomically repaints existing scroll-region rows
// without advancing the output cursor or changing its tracked position.
func (p *PromptPreservingWriter) ReplaceScrollBlockBack(linesBack int, lines []string) bool {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	if linesBack < 0 || len(lines) == 0 {
		return false
	}
	startRow := p.printLine - linesBack
	endRow := startRow + len(lines) - 1
	if startRow < 1 || endRow > p.getScrollBottom() {
		return false
	}

	if _, err := fmt.Fprint(p.inner, "\x1b7"); err != nil {
		return false
	}
	for index, line := range lines {
		if _, err := fmt.Fprintf(p.inner, "\x1b[%d;1H\x1b[2K%s", startRow+index, line); err != nil {
			_, _ = fmt.Fprint(p.inner, "\x1b8")
			return false
		}
	}
	_, err := fmt.Fprint(p.inner, "\x1b8")
	return err == nil
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
