package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"maquis/pkg/config"
	"maquis/pkg/db"
)

type renderLineCounter struct {
	bytes.Buffer
	count int
}

func (w *renderLineCounter) Write(p []byte) (int, error) {
	w.count += bytes.Count(p, []byte{'\n'})
	return w.Buffer.Write(p)
}

func (w *renderLineCounter) GetCount() int {
	return w.count
}

type wrappedRenderLineCounter struct {
	writer io.Writer
	count  int
}

func (w *wrappedRenderLineCounter) Write(p []byte) (int, error) {
	w.count += bytes.Count(p, []byte{'\n'})
	return w.writer.Write(p)
}

func (w *wrappedRenderLineCounter) GetCount() int {
	return w.count
}

func (w *wrappedRenderLineCounter) Unwrap() io.Writer {
	return w.writer
}

func TestStreamedToolTargetUsesOneStableHeader(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(`{"path": "README.md"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	if count := strings.Count(rendered, "README.md"); count != 1 {
		t.Fatalf("expected target once, got %d occurrences in %q", count, rendered)
	}
	if strings.Contains(rendered, "▸ path:") {
		t.Fatalf("target should be represented by the tool header, got %q", rendered)
	}
	if line := renderer.GetToolTitleLineNumber(0); line < 0 {
		t.Fatalf("streamed tool header was not tracked, got line %d", line)
	}
}

func TestThoughtCompletesBeforeToolHeader(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.StartToolCall("spawn_subagent", 0)
	renderer.WriteToolCall(`{"name":"devops_audit"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIndex := strings.Index(rendered, "✔ thought")
	toolIndex := strings.Index(rendered, "spawn_subagent devops_audit")
	if thoughtIndex < 0 || toolIndex < 0 {
		t.Fatalf("missing thought completion or tool header: %q", rendered)
	}
	if thoughtIndex > toolIndex {
		t.Fatalf("thought completion rendered after its tool call: %q", rendered)
	}
	if strings.Contains(rendered[thoughtIndex:toolIndex], "\n\n") {
		t.Fatalf("thought and tool header were separated by a transient blank row: %q", rendered)
	}
}

func TestThoughtUsesOneBlankRowBeforeAnswerText(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.Write("final answer")
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIndex := strings.Index(rendered, "✔ thought")
	answerIndex := strings.Index(rendered, "final answer")
	if thoughtIndex < 0 || answerIndex < 0 || thoughtIndex > answerIndex {
		t.Fatalf("missing ordered thought and answer: %q", rendered)
	}
	between := rendered[thoughtIndex:answerIndex]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("thought and answer must have exactly one blank row: %q", rendered)
	}
}

func TestCompletedThoughtHasNoTransientTrailingBlankRow(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.EndThinking()

	rendered := stripAnsi(output.String())
	if strings.HasSuffix(rendered, "\n\n") {
		t.Fatalf("completed thought retained a transient trailing blank row: %q", rendered)
	}
}

func TestLiveThoughtToolLayoutMatchesSessionHistory(t *testing.T) {
	const (
		reasoning  = "Reading `test_security.py`."
		arguments  = `{"path":"test_security.py"}`
		toolOutput = "def test_authentication():\n    pass"
	)
	theme := UITheme{}

	var live renderLineCounter
	fmt.Fprintln(&live, strings.Repeat("╌", 40))
	renderer := NewStreamRenderer(&live, theme, true, false, "test")
	renderer.WriteReasoning(reasoning)
	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	RenderToolOutput(&live, toolOutput, false, false, theme, "read", arguments, renderer.DidStreamToolBody(0))

	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "read",
			Arguments: arguments,
		},
	}
	messages := []db.Message{
		{
			Role:              "assistant",
			ReasoningContent:  reasoning,
			ReasoningDuration: renderer.GetReasoningDuration(),
			ToolCalls:         []db.ToolCall{toolCall},
		},
		{
			Role:       "tool",
			ToolCallID: toolCall.ID,
			Name:       toolCall.Function.Name,
			Content:    toolOutput,
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, theme, &config.Config{ShowThinking: true})

	liveText := strings.Replace(stripAnsi(live.String()), "▸ read", "✔ read", 1)
	historyText := stripAnsi(history.String())
	if liveText != historyText {
		t.Fatalf("live and persisted layouts differ:\nlive:    %q\nhistory: %q", liveText, historyText)
	}
}

func TestSessionHistoryUsesFinalToolStatus(t *testing.T) {
	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "read",
			Arguments: `{"path":"test_security.py"}`,
		},
	}
	messages := []db.Message{
		{Role: "assistant", ToolCalls: []db.ToolCall{toolCall}},
		{Role: "tool", ToolCallID: toolCall.ID, Name: "read", Content: "ok"},
	}

	var output bytes.Buffer
	PrintSessionHistory(&output, messages, UITheme{}, &config.Config{})

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, "✔ read test_security.py") {
		t.Fatalf("persisted successful tool header did not use its final status: %q", rendered)
	}
}

func TestSuccessfulReadOutputFollowsHeaderDirectly(t *testing.T) {
	var output bytes.Buffer
	arguments := `{"path":"ssl_checker/app/api/v1/api.py"}`

	RenderToolHeader(&output, UITheme{}, "read", arguments)
	RenderToolOutput(&output, "from fastapi import APIRouter", false, false, UITheme{}, "read", arguments, false)

	rendered := stripAnsi(output.String())
	if strings.Contains(rendered, "Output") {
		t.Fatalf("read result included a redundant output label: %q", rendered)
	}
	if strings.Contains(rendered, "api.py\n\nfrom fastapi") {
		t.Fatalf("read result included an extra blank line after its header: %q", rendered)
	}
	if !strings.Contains(rendered, "\nfrom fastapi") {
		t.Fatalf("read content did not follow its header directly: %q", rendered)
	}
}

func TestSuccessfulBashOutputFollowsHeaderDirectly(t *testing.T) {
	var output bytes.Buffer
	arguments := `{"command":"find . -maxdepth 3"}`

	RenderToolHeader(&output, UITheme{}, "bash", arguments)
	RenderToolOutput(&output, "./app\n./tests", false, false, UITheme{}, "bash", arguments, false)

	rendered := stripAnsi(output.String())
	if strings.Contains(rendered, "Output") {
		t.Fatalf("bash result included a redundant output label: %q", rendered)
	}
	if strings.Contains(rendered, "\n\n./app") {
		t.Fatalf("bash result included an extra blank line after its header: %q", rendered)
	}
	if !strings.Contains(rendered, "\n./app") {
		t.Fatalf("bash content did not follow its header directly: %q", rendered)
	}
}

func TestCollapsedResultsNeverHideToolErrors(t *testing.T) {
	var output bytes.Buffer
	const diagnostic = "npm ERR! code ERESOLVE\nnpm ERR! unable to resolve dependency tree"

	RenderToolOutput(
		&output,
		diagnostic,
		true,
		true,
		UITheme{},
		"bash",
		`{"command":"npm install"}`,
		false,
	)

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, diagnostic) {
		t.Fatalf("collapsed results hid the command diagnostic: %q", rendered)
	}
	if strings.Contains(rendered, "lines collapsed") {
		t.Fatalf("tool error was replaced by a collapsed placeholder: %q", rendered)
	}
}

func TestSequentialStreamedToolsKeepIndependentHeaders(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(`{"path":"README.md"}`)
	renderer.StartToolCall("load_skill", 1)
	renderer.WriteToolCall(`{"name":"loop-brain"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	for _, target := range []string{"README.md", "loop-brain"} {
		if count := strings.Count(rendered, target); count != 1 {
			t.Fatalf("expected %q once, got %d occurrences in %q", target, count, rendered)
		}
	}
	for index := 0; index < 2; index++ {
		if line := renderer.GetToolTitleLineNumber(index); line < 0 {
			t.Fatalf("tool %d has no independently tracked header", index)
		}
	}
}

func TestStreamedWriteContentIsNotRenderedTwice(t *testing.T) {
	const payload = "unique streamed payload"

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
	arguments := `{"path":"result.txt","content":"unique streamed payload"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	RenderToolOutput(&output, "wrote result.txt", false, false, UITheme{}, "write", arguments, true)

	rendered := stripAnsi(output.String())
	if count := strings.Count(rendered, payload); count != 1 {
		t.Fatalf("expected streamed write content once, got %d occurrences in %q", count, rendered)
	}
}

func TestEditIntentIsNotRenderedBeforeExecution(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")

	renderer.StartToolCall("edit", 0)
	for _, chunk := range []string{
		`{"path":"certificate.py","oldText":"from datetime import datetime\n`,
		`value = 1","newText":"from datetime import datetime, timezone\n`,
		`value = 2"}`,
	} {
		renderer.WriteToolCall(chunk)
	}
	renderer.Flush()

	rendered := sanitizeTerminalText(output.String())
	for _, hiddenIntent := range []string{
		"from datetime import datetime",
		"value = 1",
		"from datetime import datetime, timezone",
		"value = 2",
	} {
		if strings.Contains(rendered, hiddenIntent) {
			t.Fatalf("edit intent appeared before execution: %q in %q", hiddenIntent, rendered)
		}
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("edit intent was marked as an executed tool body")
	}
}

func TestEditDiffIsRenderedOnlyAtCompletion(t *testing.T) {
	const (
		oldLine = "value = before"
		newLine = "value = after"
	)

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
	arguments := `{"path":"model.py","updates":[{"oldText":"value = before","newText":"value = after"}]}`

	renderer.StartToolCall("edit", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	if renderer.DidStreamToolBody(0) {
		t.Fatal("edit intent was reported as streamed output")
	}

	completedDiff := "\x1b[31m1    - value = before\x1b[0m\n\x1b[32m1    + value = after\x1b[0m"
	RenderToolOutput(&output, completedDiff, false, false, UITheme{}, "edit", arguments, renderer.DidStreamToolBody(0))

	rendered := sanitizeTerminalText(output.String())
	for _, line := range []string{"- " + oldLine, "+ " + newLine} {
		if count := strings.Count(rendered, line); count != 1 {
			t.Fatalf("expected edit line %q once across stream and completion, got %d in %q", line, count, rendered)
		}
	}
}

func TestDisabledEditStreamingRendersDiffOnlyAtCompletion(t *testing.T) {
	const (
		oldLine = "value = before"
		newLine = "value = after"
	)

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")
	arguments := `{"path":"model.py","updates":[{"oldText":"value = before","newText":"value = after"}]}`

	renderer.StartToolCall("edit", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	beforeCompletion := sanitizeTerminalText(output.String())
	if strings.Contains(beforeCompletion, oldLine) || strings.Contains(beforeCompletion, newLine) {
		t.Fatalf("edit arguments appeared before completion: %q", beforeCompletion)
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("renderer reported a streamed edit body while streaming was disabled")
	}

	completedDiff := "\x1b[31m1    - value = before\x1b[0m\n\x1b[32m1    + value = after\x1b[0m"
	RenderToolOutput(&output, completedDiff, false, false, UITheme{}, "edit", arguments, renderer.DidStreamToolBody(0))

	rendered := sanitizeTerminalText(output.String())
	for _, line := range []string{"- " + oldLine, "+ " + newLine} {
		if count := strings.Count(rendered, line); count != 1 {
			t.Fatalf("expected final edit line %q once, got %d in %q", line, count, rendered)
		}
	}
}

func TestDisabledWriteStreamingRendersContentOnlyAtCompletion(t *testing.T) {
	const payload = "completion-only payload"

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")
	arguments := `{"path":"result.txt","content":"completion-only payload"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	if strings.Contains(stripAnsi(output.String()), payload) {
		t.Fatalf("write content appeared before completion: %q", stripAnsi(output.String()))
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("renderer reported a streamed body while write streaming was disabled")
	}

	RenderToolOutput(&output, "wrote result.txt", false, false, UITheme{}, "write", arguments, renderer.DidStreamToolBody(0))
	if count := strings.Count(stripAnsi(output.String()), payload); count != 1 {
		t.Fatalf("expected completion content once, got %d", count)
	}
}

func TestToolCompletionUpdatesOnlyTrackedHeaderRow(t *testing.T) {
	var terminal bytes.Buffer
	promptWriter := NewPromptPreservingWriter(&terminal, 30)
	counter := &wrappedRenderLineCounter{writer: promptWriter}
	renderer := NewStreamRenderer(counter, UITheme{}, false, false, "test")
	arguments := `{"path":"README.md"}`

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	terminal.Reset()
	renderer.CompleteToolCall(0, "read", arguments, false)

	update := terminal.String()
	if count := strings.Count(update, "\x1b[2K"); count != 1 {
		t.Fatalf("expected one header-row clear, got %d in %q", count, update)
	}
	if strings.Contains(update, "\x1b[J") {
		t.Fatalf("completion must not clear the surrounding screen: %q", update)
	}
	clean := stripAnsi(update)
	if !strings.Contains(clean, "✔") || !strings.Contains(clean, "read") || !strings.Contains(clean, "README.md") {
		t.Fatalf("completion did not update the tracked title: %q", clean)
	}
}
