package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"maquis/pkg/ui/style"
	"golang.org/x/term"
)

type StatusBarState struct {
	Model                  string
	PromptTokens           int
	CompletionTokens       int
	CurrentCompletionTokens int // Used to calculate current t/s speed
	ContextLimit           int
	StartTime              time.Time
	IsGenerating           bool
	LastTps                float64
	HasLastTps             bool
	ActiveTasksCount       int
	ShowTokens             bool
}

// SetCollapseStatus updates the results collapsing state in the status bar.
func SetCollapseStatus(collapsed bool) {
	stateMu.Lock()
	collapseResults = collapsed
	stateMu.Unlock()
}

// SetScrollRegionOffset reserves extra lines above the status bar's normal 2-line area.
// For example, offset=2 reserves lines for a prompt separator and prompt input,
// making the scroll region 1..height-4 instead of 1..height-2.
func SetScrollRegionOffset(offset int) {
	stateMu.Lock()
	scrollRegionOffset = offset
	stateMu.Unlock()
}

// ClearScrollRegionOffset resets the scroll region to the default (1..height-2).
func ClearScrollRegionOffset() {
	stateMu.Lock()
	scrollRegionOffset = 0
	stateMu.Unlock()
}

func getTerminalSize() (int, int) {
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stderr.Fd())); err == nil && h > 0 {
		return w, h
	}
	return 80, 24
}

func InitStatusBar(w io.Writer) {
	stateMu.Lock()
	enabled = true
	stateMu.Unlock()

	_, height := getTerminalSize()
	if height > 3 {
		// Print newline and cursor up to ensure we scroll if we are near the bottom
		fmt.Fprint(w, "\n\n\x1b[2A")
		// Save cursor, set scroll region, restore cursor to prevent jumping to top
		fmt.Fprintf(w, "\x1b7\x1b[1;%dr\x1b8", height-2-scrollRegionOffset)
		lastH = height
	}
}

func ShutdownStatusBar(w io.Writer) {
	stateMu.Lock()
	enabled = false
	stateMu.Unlock()

	_, height := getTerminalSize()
	var buf bytes.Buffer
	if height > 0 {
		// Clear stats line (height-4), prompt separator (height-3), status bar border (height-1) and status bar (height)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-4)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-3)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-1)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height)
		// Reset cursor position to height (bottom line)
		fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	}
	fmt.Fprint(&buf, "\x1b[r\x1b[?25h") // Reset scrolling region and show cursor
	_, _ = w.Write(buf.Bytes())
}

func UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool) {
	stateMu.Lock()
	state.Model = model
	if promptTokens >= state.PromptTokens {
		state.PromptTokens = promptTokens
	}
	if completionTokens >= state.CompletionTokens {
		state.CompletionTokens = completionTokens
	}
	state.CurrentCompletionTokens = currentCompletionTokens
	state.ContextLimit = contextLimit
	state.IsGenerating = isGenerating
	state.ActiveTasksCount = activeTasks
	state.ShowTokens = showTokens
	if tps > 0 {
		state.LastTps = tps
		state.HasLastTps = true
	} else if !isGenerating {
		state.HasLastTps = false
		state.LastTps = 0
	}
	stateMu.Unlock()
}

func DrawStatusBar(w io.Writer, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	stateMu.Lock()
	defer stateMu.Unlock()

	if !enabled {
		return
	}

	width, height := getTerminalSize()
	if height <= 3 {
		return
	}

	var buf bytes.Buffer

	// Only set the scrolling region when the terminal height changes
	scrollBottom := height - 2 - scrollRegionOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	if height != lastH {
		if lastH > 0 {
			// Clear old status bar and separator lines to prevent duplicate trails on resize
			fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[J\x1b8", lastH-1)
		}
		fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8", scrollBottom)
		lastH = height
	}

	// Save cursor
	fmt.Fprint(&buf, "\x1b7")

	// Draw separator line at height-1
	fmt.Fprintf(&buf, "\x1b[%d;1H", height-1)
	fmt.Fprint(&buf, "\x1b[2K")
	borderStyle := style.NewStyle().Foreground(theme.Border)
	borderLine := borderStyle.Render(strings.Repeat("─", width-1))
	fmt.Fprint(&buf, borderLine)
 
	// Draw status bar content at height
	fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	fmt.Fprint(&buf, "\x1b[2K")
 
	leftPart := formatLeft(theme, width)
	rightPart := formatRight(theme, width)
 
	leftLen := len(stripAnsi(leftPart))
	rightLen := len(stripAnsi(rightPart))
 
	padding := (width - 1) - leftLen - rightLen
	if padding < 1 {
		padding = 1
	}
 
	fmt.Fprintf(&buf, "%s%s%s", leftPart, strings.Repeat(" ", padding), rightPart)
 
	// Restore cursor
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func formatLeft(theme UITheme, width int) string {
	if !state.ShowTokens {
		return ""
	}
	pStrCompact := fmt.Sprintf("%d↓", state.PromptTokens)
	if state.PromptTokens >= 1000 {
		pStrCompact = fmt.Sprintf("%.1fk↓", float64(state.PromptTokens)/1000.0)
	}

	cStrCompact := fmt.Sprintf("%d↑", state.CompletionTokens)
	if state.CompletionTokens >= 1000 {
		cStrCompact = fmt.Sprintf("%.1fk↑", float64(state.CompletionTokens)/1000.0)
	}

	pStr := fmt.Sprintf("%d in", state.PromptTokens)
	if state.PromptTokens >= 1000 {
		pStr = fmt.Sprintf("%.1fk in", float64(state.PromptTokens)/1000.0)
	}

	cStr := fmt.Sprintf("%d out", state.CompletionTokens)
	if state.CompletionTokens >= 1000 {
		cStr = fmt.Sprintf("%.1fk out", float64(state.CompletionTokens)/1000.0)
	}
	if (state.IsGenerating || state.HasLastTps) && state.LastTps > 0 {
		cStr += fmt.Sprintf(" (%.1f t/s)", state.LastTps)
	}

	totalTokens := state.PromptTokens + state.CompletionTokens
	var pct float64
	if state.ContextLimit > 0 {
		pct = (float64(totalTokens) / float64(state.ContextLimit)) * 100.0
	}

	totStr := fmt.Sprintf("%d", totalTokens)
	if totalTokens >= 1000 {
		totStr = fmt.Sprintf("%.1fk", float64(totalTokens)/1000.0)
	}

	limitStr := fmt.Sprintf("%d", state.ContextLimit)
	if state.ContextLimit >= 1000 {
		limitStr = fmt.Sprintf("%dk", state.ContextLimit/1000)
	}

	var ctxStr string
	if width < 70 {
		ctxStr = fmt.Sprintf("%s/%s", totStr, limitStr)
	} else {
		ctxStr = fmt.Sprintf("%s/%s (%.1f%%)", totStr, limitStr, pct)
	}

	if width < 40 {
		pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStrCompact)
		cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStrCompact)
		return fmt.Sprintf(" %s %s", pStyled, cStyled)
	} else if width < 55 {
		pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStr)
		cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStr)
		return fmt.Sprintf(" %s  %s", pStyled, cStyled)
	} else if width < 75 {
		pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStr)
		cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStr)
		ctxStyled := style.NewStyle().Foreground(theme.Primary).Render(ctxStr)
		return fmt.Sprintf(" %s   %s   %s", pStyled, cStyled, ctxStyled)
	} else {
		pStr = fmt.Sprintf("%-9s", pStr)
		cStr = fmt.Sprintf("%-21s", cStr)
		ctxStr = fmt.Sprintf("%-28s", ctxStr)

		pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStr)
		cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStr)
		ctxStyled := style.NewStyle().Foreground(theme.Primary).Render(ctxStr)
		return fmt.Sprintf(" %s   %s   %s", pStyled, cStyled, ctxStyled)
	}
}

func formatRight(theme UITheme, width int) string {
	if state.Model == "" {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	collapseStr := ""
	if collapseResults {
		collapseStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		if width < 40 {
			collapseStr = collapseStyle.Render("c") + " "
		} else if width < 60 {
			collapseStr = collapseStyle.Render("[c]") + " "
		} else {
			collapseStr = collapseStyle.Render("[collapsed]") + " "
		}
	}

	taskStr := ""
	if state.ActiveTasksCount > 0 {
		taskStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		if width < 40 {
			taskStr = taskStyle.Render(fmt.Sprintf("t:%d", state.ActiveTasksCount)) + " "
		} else if width < 60 {
			taskStr = taskStyle.Render(fmt.Sprintf("[t:%d]", state.ActiveTasksCount)) + " "
		} else {
			taskStr = taskStyle.Render(fmt.Sprintf("[tasks:%d]", state.ActiveTasksCount)) + " "
		}
	}

	if width < 45 {
		return collapseStr + taskStr
	} else if width < 65 {
		modelName := state.Model
		if len(modelName) > 10 {
			modelName = modelName[:10] + "..."
		}
		return collapseStr + taskStr + modelStyle.Render(modelName) + " "
	} else {
		return collapseStr + taskStr + modelStyle.Render(state.Model) + " "
	}
}

func stripAnsi(str string) string {
	var sb strings.Builder
	inEscape := false
	for i := 0; i < len(str); i++ {
		if str[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if str[i] == 'm' {
				inEscape = false
			}
			continue
		}
		sb.WriteByte(str[i])
	}
	return sb.String()
}
