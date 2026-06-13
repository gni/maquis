package ui

import (
	"io"

	"bidouille/pkg/agent"
	"bidouille/pkg/ui/style"
)

type AgentUIImpl struct{}

func (ui *AgentUIImpl) DrawStatusBar(w io.Writer, theme style.UITheme) {
	DrawStatusBar(w, theme)
}

func (ui *AgentUIImpl) DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme) {
	DrawStaticPromptSeparator(w, showThinking, reasoningEffort, theme)
}

func (ui *AgentUIImpl) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool) agent.StreamRenderer {
	return NewStreamRenderer(w, theme, showThinking)
}

func (ui *AgentUIImpl) SetCollapseStatus(collapsed bool) {
	SetCollapseStatus(collapsed)
}

func (ui *AgentUIImpl) UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int) {
	UpdateStatus(model, promptTokens, completionTokens, currentCompletionTokens, contextLimit, isGenerating, tps, activeTasks)
}

func (ui *AgentUIImpl) AskForApproval(w io.Writer, theme style.UITheme) (bool, bool) {
	return AskForApproval(w, theme)
}

func (ui *AgentUIImpl) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, highlightLines int) {
	RenderToolOutput(w, output, isError, collapseResults, theme, toolName, toolArgs, highlightLines)
}
