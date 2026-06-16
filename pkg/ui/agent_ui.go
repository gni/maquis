package ui

import (
	"io"

	"maquis/pkg/agent"
	"maquis/pkg/ui/style"
)

type AgentUIImpl struct{}

func (ui *AgentUIImpl) DrawStatusBar(w io.Writer, theme style.UITheme) {
	DrawStatusBar(w, theme)
}

func (ui *AgentUIImpl) DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string) {
	DrawStaticPromptSeparatorWithSpinner(w, showThinking, reasoningEffort, theme, spinnerFrame)
}

func (ui *AgentUIImpl) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool) agent.StreamRenderer {
	return NewStreamRenderer(w, theme, showThinking, streamWrites)
}

func (ui *AgentUIImpl) SetCollapseStatus(collapsed bool) {
	SetCollapseStatus(collapsed)
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

func (ui *AgentUIImpl) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, highlightLines int) {
	RenderToolOutput(w, output, isError, collapseResults, theme, toolName, toolArgs, highlightLines)
}
