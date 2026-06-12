package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"bidouille/pkg/ui/style"
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
}

var (
	state      StatusBarState
	stateMu    sync.Mutex
	lastH      int
	enabled    bool
)

func getTerminalSize() (int, int) {
	if w, h, err := term.GetSize(int(os.Stderr.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && h > 0 {
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
		fmt.Fprintf(w, "\x1b[s\x1b[1;%dr\x1b[u", height-2)
		lastH = height
	}
}

func ShutdownStatusBar(w io.Writer) {
	stateMu.Lock()
	enabled = false
	stateMu.Unlock()

	fmt.Fprint(w, "\x1b[r") // Reset scrolling region
	_, height := getTerminalSize()
	if height > 0 {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K\x1b[%d;1H\x1b[2K", height-1, height)
	}
}

func UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64) {
	stateMu.Lock()
	state.Model = model
	state.PromptTokens = promptTokens
	state.CompletionTokens = completionTokens
	state.CurrentCompletionTokens = currentCompletionTokens
	state.ContextLimit = contextLimit
	state.IsGenerating = isGenerating
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
	stateMu.Lock()
	defer stateMu.Unlock()

	if !enabled {
		return
	}

	width, height := getTerminalSize()
	if height <= 3 {
		return
	}

	// Always set the scrolling region to ensure it is not reset/overwritten by the terminal
	fmt.Fprintf(w, "\x1b[s\x1b[1;%dr\x1b[u", height-2)
	lastH = height

	// Save cursor
	fmt.Fprint(w, "\x1b[s")

	// Draw separator line at height-1
	fmt.Fprintf(w, "\x1b[%d;1H", height-1)
	fmt.Fprint(w, "\x1b[2K")
	borderStyle := style.NewStyle().Foreground(theme.Border)
	borderLine := borderStyle.Render(strings.Repeat("─", width-1))
	fmt.Fprint(w, borderLine)
 
	// Draw status bar content at height
	fmt.Fprintf(w, "\x1b[%d;1H", height)
	fmt.Fprint(w, "\x1b[2K")
 
	leftPart := formatLeft(theme)
	rightPart := formatRight(theme)
 
	leftLen := len(stripAnsi(leftPart))
	rightLen := len(stripAnsi(rightPart))
 
	padding := (width - 1) - leftLen - rightLen
	if padding < 1 {
		padding = 1
	}
 
	fmt.Fprintf(w, "%s%s%s", leftPart, strings.Repeat(" ", padding), rightPart)
 
	// Restore cursor
	fmt.Fprint(w, "\x1b[u")
}

func formatLeft(theme UITheme) string {
	pStr := fmt.Sprintf("%d in", state.PromptTokens)
	if state.PromptTokens >= 1000 {
		pStr = fmt.Sprintf("%.1fk in", float64(state.PromptTokens)/1000.0)
	}
	pStr = fmt.Sprintf("%-9s", pStr)

	cStr := fmt.Sprintf("%d out", state.CompletionTokens)
	if state.CompletionTokens >= 1000 {
		cStr = fmt.Sprintf("%.1fk out", float64(state.CompletionTokens)/1000.0)
	}

	if (state.IsGenerating || state.HasLastTps) && state.LastTps > 0 {
		cStr += fmt.Sprintf(" (%.1f t/s)", state.LastTps)
	}
	cStr = fmt.Sprintf("%-21s", cStr)

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

	ctxStr := fmt.Sprintf("Context: %s/%s (%.1f%%)", totStr, limitStr, pct)
	ctxStr = fmt.Sprintf("%-28s", ctxStr)

	pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStr)
	cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStr)
	ctxStyled := style.NewStyle().Foreground(theme.Primary).Render(ctxStr)

	return fmt.Sprintf(" %s   %s   %s", pStyled, cStyled, ctxStyled)
}

func formatRight(theme UITheme) string {
	if state.Model == "" {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	return modelStyle.Render(state.Model) + " "
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
