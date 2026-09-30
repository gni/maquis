package agent

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"maquis/pkg/ui/style"
)

type mockTurnLoaderUI struct {
	mu        sync.Mutex
	drawCalls []struct {
		frame string
		text  string
	}
}

func (m *mockTurnLoaderUI) InitStatusBar(w io.Writer)     {}
func (m *mockTurnLoaderUI) ShutdownStatusBar(w io.Writer) {}
func (m *mockTurnLoaderUI) DrawStatusBar(w io.Writer, theme style.UITheme) {
}
func (m *mockTurnLoaderUI) DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string) {
}
func (m *mockTurnLoaderUI) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) StreamRenderer {
	return nil
}
func (m *mockTurnLoaderUI) SetCollapseStatus(collapsed bool) {}
func (m *mockTurnLoaderUI) UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool) {
}
func (m *mockTurnLoaderUI) DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drawCalls = append(m.drawCalls, struct {
		frame string
		text  string
	}{frame: spinnerFrame, text: statsText})
}
func (m *mockTurnLoaderUI) AskForApproval(w io.Writer, theme style.UITheme) (bool, bool) {
	return true, false
}
func (m *mockTurnLoaderUI) AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) SubagentCancellationDecision {
	return SubagentCancellationContinue
}
func (m *mockTurnLoaderUI) RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string) {
}
func (m *mockTurnLoaderUI) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool) {
}
func (m *mockTurnLoaderUI) SetCursorHidden(hidden bool) {}

func TestTurnLoaderTimer(t *testing.T) {
	mockUI := &mockTurnLoaderUI{}
	a := &Agent{
		UI: mockUI,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTime := time.Now().Add(-2 * time.Second) // 2.0s elapsed
	var buf bytes.Buffer
	theme := style.UITheme{}

	loader := a.newTurnLoader(ctx, &buf, theme, startTime)
	if loader == nil {
		t.Fatal("expected loader to not be nil")
	}

	// Wait briefly for initial draw
	time.Sleep(50 * time.Millisecond)

	mockUI.mu.Lock()
	initialCount := len(mockUI.drawCalls)
	if initialCount == 0 {
		mockUI.mu.Unlock()
		t.Fatal("expected at least one draw call from initial render")
	}
	firstCall := mockUI.drawCalls[0]
	mockUI.mu.Unlock()

	// Initial render should have dot frame and timer >= 2.0s
	strippedTimer := style.StripAnsi(firstCall.text)
	if !strings.HasPrefix(strippedTimer, "(") || !strings.HasSuffix(strippedTimer, "s)") {
		t.Errorf("expected timer format (X.Xs), got %q", strippedTimer)
	}
	if !strings.Contains(firstCall.frame, "•") {
		t.Errorf("expected frame to contain bullet, got %q", firstCall.frame)
	}

	// Pause dots (e.g. streaming write active without loading dots)
	loader.PauseDots()
	time.Sleep(20 * time.Millisecond)

	mockUI.mu.Lock()
	lastCall := mockUI.drawCalls[len(mockUI.drawCalls)-1]
	mockUI.mu.Unlock()

	if lastCall.frame != "" {
		t.Errorf("expected empty frame when dots paused, got %q", lastCall.frame)
	}
	strippedTimer = style.StripAnsi(lastCall.text)
	if !strings.HasPrefix(strippedTimer, "(") || !strings.HasSuffix(strippedTimer, "s)") {
		t.Errorf("expected timer to continue showing when dots paused, got %q", strippedTimer)
	}

	// Show dots (e.g. tool execution or stream hung)
	loader.ShowDots()
	time.Sleep(20 * time.Millisecond)

	mockUI.mu.Lock()
	lastCall = mockUI.drawCalls[len(mockUI.drawCalls)-1]
	mockUI.mu.Unlock()

	if !strings.Contains(lastCall.frame, "•") {
		t.Errorf("expected frame to contain bullet when dots resumed, got %q", lastCall.frame)
	}

	// Stop loader
	loader.Stop()
	time.Sleep(20 * time.Millisecond)

	mockUI.mu.Lock()
	finalCall := mockUI.drawCalls[len(mockUI.drawCalls)-1]
	mockUI.mu.Unlock()

	if finalCall.frame != "" || finalCall.text != "" {
		t.Errorf("expected empty frame and text on stop, got frame=%q, text=%q", finalCall.frame, finalCall.text)
	}
}
