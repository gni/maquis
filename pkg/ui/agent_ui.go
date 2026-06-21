package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/ui/style"
)

// AgentUIImpl implements agent.AgentUI and encapsulates the state of the TUI.
type AgentUIImpl struct {
	Config             *config.Config
	Theme              style.UITheme
	SessionID          string

	ActiveCancelFunc   context.CancelFunc
	PasteLinesOffset   int
	ActiveInputReader  io.Reader
	InApprovalPrompt   bool
	State              StatusBarState
	LastH              int
	Enabled            bool
	ScrollRegionOffset int
	CollapseResults    bool
	LastStatsText      string
	IsInteractive      bool

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
	ui.Enabled = false
	ui.StateMu.Unlock()

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

func (ui *AgentUIImpl) RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string) {
	RenderToolHeader(w, theme, toolName, toolArgs)
}

func (ui *AgentUIImpl) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, highlightLines int) {
	RenderToolOutput(w, output, isError, collapseResults, theme, toolName, toolArgs, highlightLines)
}
