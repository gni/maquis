package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
	"maquis/pkg/ui/style"
)

type StatusBarState struct {
	Model                   string
	PromptTokens            int
	CompletionTokens        int
	CurrentCompletionTokens int // Used to calculate current t/s speed
	ContextLimit            int
	StartTime               time.Time
	IsGenerating            bool
	TokenEstimate           bool
	LastTps                 float64
	HasLastTps              bool
	ActiveTasksCount        int
	ShowTokens              bool
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

func UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool, tokenEstimate ...bool) {
	estimated := isGenerating
	if len(tokenEstimate) > 0 {
		estimated = tokenEstimate[0]
	}

	getUI().StateMu.Lock()
	getUI().State.Model = model
	if promptTokens >= 0 {
		getUI().State.PromptTokens = promptTokens
	}
	if completionTokens >= 0 {
		getUI().State.CompletionTokens = completionTokens
	}
	getUI().State.CurrentCompletionTokens = currentCompletionTokens
	getUI().State.ContextLimit = contextLimit
	getUI().State.IsGenerating = isGenerating
	getUI().State.TokenEstimate = estimated
	getUI().State.ActiveTasksCount = activeTasks
	getUI().State.ShowTokens = showTokens
	if tps > 0 {
		getUI().State.LastTps = tps
		getUI().State.HasLastTps = true
	} else if !isGenerating {
		getUI().State.HasLastTps = false
		getUI().State.LastTps = 0
	}
	getUI().StateMu.Unlock()
}

func DrawStatusBar(w io.Writer, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStatusBarLocked(w, theme)
}

func DrawStatusBarLocked(w io.Writer, theme UITheme) {
	getUI().StateMu.Lock()
	defer getUI().StateMu.Unlock()

	if !getUI().Enabled {
		return
	}

	width, height := getTerminalSize()
	if height <= 3 {
		return
	}

	var buf bytes.Buffer

	offset := getUI().ScrollRegionOffset
	scrollBottom := height - 2 - offset - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	if height != getUI().LastH || getUI().PasteLinesOffset != getUI().LastPasteLinesOffset {
		if getUI().LastH > 0 && height != getUI().LastH {
			// Clear old status bar and separator lines to prevent duplicate trails on resize
			clearStart := getUI().LastH - 4
			if clearStart < 1 {
				clearStart = 1
			}
			fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[J\x1b8", clearStart)
		}
		getUI().LastH = height
		getUI().LastPasteLinesOffset = getUI().PasteLinesOffset
		getUI().LastStatusBarText = "" // Force redraw of the actual text
	}
	fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8", scrollBottom)

	// Save cursor
	fmt.Fprint(&buf, "\x1b7")

	leftPart := formatLeft(theme, width)
	rightPart := formatRight(theme, width)

	leftLen := len(stripAnsi(leftPart))
	rightLen := len(stripAnsi(rightPart))

	padding := (width - 1) - leftLen - rightLen
	if padding < 1 {
		padding = 1
	}

	newStatusBarText := fmt.Sprintf("%s%s%s", leftPart, strings.Repeat(" ", padding), rightPart)

	indicator := "▼"
	if getUI().CollapseResults {
		indicator = "▸"
	}
	deltaKey := newStatusBarText + indicator

	// Line-Level Delta Rendering check
	if deltaKey == getUI().LastStatusBarText && height == getUI().LastH {
		return
	}
	getUI().LastStatusBarText = deltaKey

	// Draw separator line at height-1
	fmt.Fprintf(&buf, "\x1b[%d;1H", height-1)
	fmt.Fprint(&buf, "\x1b[2K")
	borderStyle := style.NewStyle().Foreground(theme.Border)
	collapseStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

	dashesCount := (width - 1) - 2 // space + indicator
	if dashesCount < 1 {
		dashesCount = 1
	}
	borderLine := borderStyle.Render(strings.Repeat("─", dashesCount)) + " " + collapseStyle.Render(indicator)
	fmt.Fprint(&buf, borderLine)

	// Draw status bar content at height
	fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	fmt.Fprint(&buf, "\x1b[2K")

	fmt.Fprint(&buf, newStatusBarText)

	// Restore cursor
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func formatLeft(theme UITheme, width int) string {
	if !getUI().State.ShowTokens {
		return ""
	}
	pStrCompact := fmt.Sprintf("%d↓", getUI().State.PromptTokens)
	if getUI().State.PromptTokens >= 1000 {
		pStrCompact = fmt.Sprintf("%.1fk↓", float64(getUI().State.PromptTokens)/1000.0)
	}

	cStrCompact := fmt.Sprintf("%d↑", getUI().State.CompletionTokens)
	if getUI().State.CompletionTokens >= 1000 {
		cStrCompact = fmt.Sprintf("%.1fk↑", float64(getUI().State.CompletionTokens)/1000.0)
	}

	pStr := fmt.Sprintf("%d in", getUI().State.PromptTokens)
	if getUI().State.PromptTokens >= 1000 {
		pStr = fmt.Sprintf("%.1fk in", float64(getUI().State.PromptTokens)/1000.0)
	}

	cStr := fmt.Sprintf("%d out", getUI().State.CompletionTokens)
	if getUI().State.CompletionTokens >= 1000 {
		cStr = fmt.Sprintf("%.1fk out", float64(getUI().State.CompletionTokens)/1000.0)
	}
	if (getUI().State.IsGenerating || getUI().State.HasLastTps) && getUI().State.LastTps > 0 {
		cStr += fmt.Sprintf(" (%.1f t/s)", getUI().State.LastTps)
	}

	totalTokens := getUI().State.PromptTokens + getUI().State.CompletionTokens
	var pct float64
	if getUI().State.ContextLimit > 0 {
		pct = (float64(totalTokens) / float64(getUI().State.ContextLimit)) * 100.0
	}

	totStr := fmt.Sprintf("%d", totalTokens)
	if totalTokens >= 1000 {
		totStr = fmt.Sprintf("%.1fk", float64(totalTokens)/1000.0)
	}
	pctStr := fmt.Sprintf("%.1f%%", pct)
	if getUI().State.IsGenerating || getUI().State.TokenEstimate {
		totStr = "~" + totStr
		pctStr = "~" + pctStr
	}

	limitStr := fmt.Sprintf("%d", getUI().State.ContextLimit)
	if getUI().State.ContextLimit >= 1000 {
		limitStr = fmt.Sprintf("%dk", getUI().State.ContextLimit/1000)
	}

	var ctxStr string
	if width < 70 {
		ctxStr = fmt.Sprintf("%s/%s", totStr, limitStr)
	} else {
		ctxStr = fmt.Sprintf("%s/%s (%s)", totStr, limitStr, pctStr)
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
	if getUI().State.Model == "" {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	taskStr := ""
	if getUI().State.ActiveTasksCount > 0 {
		taskStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		if width < 40 {
			taskStr = taskStyle.Render(fmt.Sprintf("t:%d", getUI().State.ActiveTasksCount)) + " "
		} else if width < 60 {
			taskStr = taskStyle.Render(fmt.Sprintf("[t:%d]", getUI().State.ActiveTasksCount)) + " "
		} else {
			taskStr = taskStyle.Render(fmt.Sprintf("[tasks:%d]", getUI().State.ActiveTasksCount)) + " "
		}
	}

	if width < 45 {
		return taskStr
	} else if width < 65 {
		modelName := getUI().State.Model
		if len(modelName) > 10 {
			modelName = modelName[:10] + "..."
		}
		return taskStr + modelStyle.Render(modelName) + " "
	} else {
		return taskStr + modelStyle.Render(getUI().State.Model) + " "
	}
}
