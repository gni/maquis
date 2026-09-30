package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/ui/style"
)

// AgentUIImpl implements agent.AgentUI and encapsulates the state of the TUI.
type AgentUIImpl struct {
	Config    *config.Config
	Theme     style.UITheme
	SessionID string
	ppWriter  *PromptPreservingWriter

	ActiveCancelFunc     context.CancelFunc
	PasteLinesOffset     int
	PasteCounter         int
	ActiveInputReader    io.Reader
	InApprovalPrompt     bool
	State                StatusBarState
	LastH                int
	LastPasteLinesOffset int
	Enabled              bool
	ScrollRegionOffset   int
	CollapseResults      bool
	LastStatsText        string
	LastStatusBarText    string
	IsInteractive        bool
	PromptHint           string
	LastCtrlD            time.Time
	CtrlDTimer           *time.Timer
	TriggerRedraw        func()

	StateMu    sync.Mutex
	TerminalMu sync.Mutex
}

// NewAgentUI initializes a new TUI instance and registers it as the active UI.
func NewAgentUI(cfg *config.Config, theme style.UITheme) *AgentUIImpl {
	uiImpl := &AgentUIImpl{
		Config:          cfg,
		Theme:           theme,
		CollapseResults: cfg.CollapseResults,
	}
	ActiveUI = uiImpl
	return uiImpl
}

func (ui *AgentUIImpl) CancelActiveOperation() bool {
	ui.StateMu.Lock()
	cancel := ui.ActiveCancelFunc
	ui.StateMu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}

func (ui *AgentUIImpl) SetCollapseStatus(collapsed bool) {
	ui.StateMu.Lock()
	ui.CollapseResults = collapsed
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) SetScrollRegionOffset(offset int) {
	ui.StateMu.Lock()
	ui.ScrollRegionOffset = offset
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) ClearScrollRegionOffset() {
	ui.StateMu.Lock()
	ui.ScrollRegionOffset = 0
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) InitStatusBar(w io.Writer) {
	ui.StateMu.Lock()
	ui.Enabled = true
	ui.StateMu.Unlock()

	_, height := getTerminalSize()
	if height > 3 {
		// Print newline and cursor up to ensure we scroll if we are near the bottom
		fmt.Fprint(w, "\n\n\x1b[2A")
		// Save cursor, set scroll region, restore cursor to prevent jumping to top
		fmt.Fprintf(w, "\x1b7\x1b[1;%dr\x1b8", height-2-ui.ScrollRegionOffset)
		ui.LastH = height
	}
}

func (ui *AgentUIImpl) ShutdownStatusBar(w io.Writer) {
	ui.StateMu.Lock()
	if !ui.Enabled {
		ui.StateMu.Unlock()
		return
	}
	ui.Enabled = false
	ui.StateMu.Unlock()

	_, height := getTerminalSize()
	var buf bytes.Buffer
	if height > 0 {
		// Clear stats line (height-4), prompt separator (height-3), prompt line (height-2), status bar border (height-1) and status bar (height)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-4)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-3)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-2)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height-1)
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", height)
	}
	// Reset scrolling region (moves cursor to 1,1) and show cursor
	fmt.Fprint(&buf, "\x1b[r\x1b[?25h")
	if height > 4 {
		// Reposition cursor to height-4 AFTER \x1b[r where the cleared UI controls began
		// so goodbye message and shell prompt appear cleanly right below conversation history
		fmt.Fprintf(&buf, "\x1b[%d;1H", height-4)
	} else if height > 0 {
		fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	}
	_, _ = w.Write(buf.Bytes())
}

// Implement the rest of agent.AgentUI interface by calling the package-level drawing functions.
func (ui *AgentUIImpl) DrawStatusBar(w io.Writer, theme style.UITheme) {
	DrawStatusBar(w, theme)
}

func (ui *AgentUIImpl) DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string) {
	DrawStaticPromptSeparatorWithSpinner(w, showThinking, reasoningEffort, theme, spinnerFrame)
}

func (ui *AgentUIImpl) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) agent.StreamRenderer {
	return NewStreamRenderer(w, theme, showThinking, streamWrites, agentName)
}

func (ui *AgentUIImpl) UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool) {
	UpdateStatus(model, promptTokens, completionTokens, currentCompletionTokens, contextLimit, isGenerating, tps, activeTasks, showTokens)
}

func (ui *AgentUIImpl) DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string) {
	DrawStaticStatsLine(w, theme, spinnerFrame, statsText)
}

func (ui *AgentUIImpl) AskForApproval(w io.Writer, theme style.UITheme) (bool, bool) {
	return AskForApproval(w, theme)
}

func (ui *AgentUIImpl) AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) agent.SubagentCancellationDecision {
	return AskForSubagentCancellation(w, theme, agentName)
}

func (ui *AgentUIImpl) RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string) {
	RenderToolHeader(w, theme, toolName, toolArgs)
}

func (ui *AgentUIImpl) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool) {
	RenderToolOutput(w, output, isError, collapseResults, theme, toolName, toolArgs, bodyWasStreamed)
}

func (ui *AgentUIImpl) SetCursorHidden(hidden bool) {
	if ui.ppWriter != nil {
		ui.ppWriter.SetCursorHidden(hidden)
	}
}

// SetPromptHint sets a transient hint message to be displayed at the prompt,
// optionally scheduling a timer to clear the hint and call onExpire if not cancelled.
func (ui *AgentUIImpl) SetPromptHint(hint string, d time.Duration, onExpire func()) {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if ui.CtrlDTimer != nil {
		ui.CtrlDTimer.Stop()
		ui.CtrlDTimer = nil
	}
	ui.PromptHint = hint
	ui.LastCtrlD = time.Now()
	if d > 0 && onExpire != nil {
		targetTime := ui.LastCtrlD
		ui.CtrlDTimer = time.AfterFunc(d, func() {
			ui.StateMu.Lock()
			if ui.LastCtrlD.Equal(targetTime) {
				ui.PromptHint = ""
				ui.LastCtrlD = time.Time{}
				ui.CtrlDTimer = nil
				ui.StateMu.Unlock()
				onExpire()
			} else {
				ui.StateMu.Unlock()
			}
		})
	}
}

// ClearPromptHint cancels any active prompt hint and timer immediately.
func (ui *AgentUIImpl) ClearPromptHint() {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if ui.CtrlDTimer != nil {
		ui.CtrlDTimer.Stop()
		ui.CtrlDTimer = nil
	}
	ui.PromptHint = ""
	ui.LastCtrlD = time.Time{}
}

// CheckCtrlDConfirmation checks if Ctrl+D was pressed within the timeout window.
// If confirmed, it clears the hint state and returns true.
func (ui *AgentUIImpl) CheckCtrlDConfirmation(timeout time.Duration) bool {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if !ui.LastCtrlD.IsZero() && time.Since(ui.LastCtrlD) <= timeout {
		if ui.CtrlDTimer != nil {
			ui.CtrlDTimer.Stop()
			ui.CtrlDTimer = nil
		}
		ui.PromptHint = ""
		ui.LastCtrlD = time.Time{}
		return true
	}
	return false
}

