package agent

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// promptPreservingWriter wraps an io.Writer (typically os.Stderr) and ensures
// that all streamed output is confined to the terminal's scrolling region
// (rows 1 through height-4). After every write, the cursor is repositioned
// back to the prompt input line (height-2, column 3) so it blinks stably
// at the "> " prompt — even while tokens are streaming above it.
//
// The bottom 4 lines of the terminal are reserved:
//   - height-3: ─── Prompt ──────────────────────────────[reasoning:low]
//   - height-2: > (input line, cursor blinks here)
//   - height-1: ────────────────────────────────────────── (status separator)
//   - height:   status bar content
type promptPreservingWriter struct {
	inner     io.Writer
	height    int
	printLine int // current row inside the scroll region (always == scrollBottom)
	printCol  int // current column on printLine (1-based)
}

func newPromptPreservingWriter(w io.Writer, height int) *promptPreservingWriter {
	scrollBottom := height - 4
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	return &promptPreservingWriter{
		inner:     w,
		height:    height,
		printLine: scrollBottom,
		printCol:  1,
	}
}

func (p *promptPreservingWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	// Move cursor to the current print position inside the scroll region.
	// We always print at the bottom of the scroll region; the terminal
	// scrolls content up when we emit newlines.
	fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.printLine, p.printCol)

	// Write the actual content. The terminal's scrolling region (set to 1..height-4)
	// ensures that newlines here only scroll within that region.
	n, err := p.inner.Write(data)

	// Track the column position so we can restore it accurately next time.
	p.trackPosition(data[:n])

	// Restore cursor to the prompt input line (height-2, col 3 — after "> ")
	fmt.Fprintf(p.inner, "\x1b[%d;3H", p.height-2)

	return n, err
}

// trackPosition updates printCol by analyzing the written bytes.
// On newline, printCol resets to 1 (the terminal scrolls within the region).
// On carriage return, printCol resets to 1 without scrolling.
func (p *promptPreservingWriter) trackPosition(data []byte) {
	s := string(data)
	// Strip ANSI escape sequences for column tracking
	clean := stripAnsiSeqs(s)

	for _, r := range clean {
		switch r {
		case '\n':
			p.printCol = 1
		case '\r':
			p.printCol = 1
		case '\b':
			if p.printCol > 1 {
				p.printCol--
			}
		case '\t':
			// Tab stops every 8 columns
			p.printCol = ((p.printCol - 1) / 8 * 8) + 8 + 1
		default:
			if r >= 32 { // printable
				p.printCol++
			}
		}
	}

	// Clamp printCol based on terminal width to handle auto-wrap correctly
	width := 80
	if fd := int(os.Stderr.Fd()); term.IsTerminal(fd) {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			width = w
		}
	}
	if p.printCol > width {
		p.printCol = ((p.printCol - 1) % width) + 1
	}
}

// stripAnsiSeqs removes ANSI escape sequences (CSI, OSC, etc.) from a string
// so we can accurately count visible character widths.
func stripAnsiSeqs(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))

	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i++
			if i < len(s) {
				if s[i] == '[' {
					// CSI sequence: consume until a letter in @-~ range
					i++
					for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
						i++
					}
					if i < len(s) {
						i++ // consume the final byte
					}
				} else if s[i] == ']' {
					// OSC sequence: consume until ST (ESC \ or BEL)
					i++
					for i < len(s) {
						if s[i] == '\x07' {
							i++
							break
						}
						if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
							i += 2
							break
						}
						i++
					}
				} else {
					// Other escape (e.g., ESC ( B): consume next byte
					i++
				}
			}
		} else {
			sb.WriteByte(s[i])
			i++
		}
	}

	return sb.String()
}

func (p *promptPreservingWriter) Height() int {
	return p.height
}

func NewPromptPreservingWriter(w io.Writer, height int) io.Writer {
	return newPromptPreservingWriter(w, height)
}
