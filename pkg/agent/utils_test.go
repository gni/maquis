package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
)

func TestFormatDefensiveErrorExplainsOldTextMismatch(t *testing.T) {
	formatted := FormatDefensiveError(
		"edit",
		errors.New("edit[0]: oldText block was not found in file app/models/user.py"),
	)

	if strings.Contains(formatted, "directory structure") {
		t.Fatalf("oldText mismatch was misclassified as a missing path: %q", formatted)
	}
	for _, expected := range []string{"file exists", "current contents", "Read the file again", "Do not recover", "write"} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("oldText mismatch omitted %q: %q", expected, formatted)
		}
	}
}

func TestFormatDefensiveErrorStillExplainsMissingPath(t *testing.T) {
	formatted := FormatDefensiveError("read", errors.New("no such file: missing.py"))
	if !strings.Contains(formatted, "directory structure") {
		t.Fatalf("missing path lost its path-specific recommendation: %q", formatted)
	}
}

func TestFormatToolExecutionFailurePreservesCommandDiagnostics(t *testing.T) {
	diagnostic := "npm ERR! code ERESOLVE\nnpm ERR! unable to resolve dependency tree"
	formatted := FormatToolExecutionFailure("bash", diagnostic, errors.New("command failed: exit status 1"))

	for _, expected := range []string{
		"npm ERR! code ERESOLVE",
		"unable to resolve dependency tree",
		"System Alert:",
		"exit status 1",
		"Recommendation:",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("tool failure omitted %q: %q", expected, formatted)
		}
	}
	if strings.Index(formatted, diagnostic) > strings.Index(formatted, "System Alert:") {
		t.Fatalf("tool diagnostic appeared after the generic alert: %q", formatted)
	}
}

func TestFormatToolExecutionFailureDoesNotRepeatGenericFailure(t *testing.T) {
	err := errors.New("command failed: exit status 1")
	formatted := FormatToolExecutionFailure("bash", err.Error(), err)

	if count := strings.Count(formatted, err.Error()); count != 1 {
		t.Fatalf("generic failure appeared %d times: %q", count, formatted)
	}
}

func TestGetGlobalTokensUsesLatestTurnWithoutDoubleCountingPriorCompletions(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first prompt"},
		{Role: "assistant", Content: "first response", PromptTokens: 100, CompletionTokens: 20},
		{Role: "user", Content: "second prompt"},
		{Role: "assistant", Content: "second response", PromptTokens: 160, CompletionTokens: 30},
	}

	prompt, completion := a.GetGlobalTokens(messages, nil)
	if prompt != 160 || completion != 30 {
		t.Fatalf("latest context usage = (%d, %d); want (160, 30)", prompt, completion)
	}

	messages = append(messages, db.Message{Role: "user", Content: "12345678"})
	prompt, completion = a.GetGlobalTokens(messages, nil)
	if prompt != 162 || completion != 30 {
		t.Fatalf("context with pending user message = (%d, %d); want (162, 30)", prompt, completion)
	}
}

func TestGetGlobalTokensIgnoresLegacyEmptyCancellationRecord(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant", Content: "completed response", PromptTokens: 70000, CompletionTokens: 800},
		{Role: "tool", Content: strings.Repeat("x", 100)},
		{Role: "assistant", PromptTokens: 1, CompletionTokens: 1},
	}

	prompt, completion := a.GetGlobalTokens(messages, nil)
	if prompt != 70025 || completion != 800 {
		t.Fatalf("context anchored to empty cancellation record: got (%d, %d), want (70025, 800)", prompt, completion)
	}
}

func TestGetGlobalTokenUsageUsesTransmittedCompactToolDefinitions(t *testing.T) {
	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool())
	registry.Register(tool.NewBashTool())

	messages := []db.Message{{Role: "system", Content: strings.Repeat("s", 40)}}
	compactAgent := &Agent{
		Config:   &config.Config{CompactPrompt: true},
		Registry: registry,
	}
	fullAgent := &Agent{
		Config:   &config.Config{CompactPrompt: false},
		Registry: registry,
	}

	compactPrompt, compactCompletion, compactEstimated := compactAgent.GetGlobalTokenUsage(messages, nil)
	fullPrompt, _, fullEstimated := fullAgent.GetGlobalTokenUsage(messages, nil)

	compactDefinitions := prepareToolDefinitions(registry.GetAvailableTools(nil), true)
	compactJSON, err := json.Marshal(compactDefinitions)
	if err != nil {
		t.Fatalf("marshal compact tool definitions: %v", err)
	}
	wantCompactPrompt := len(messages[0].Content)/4 + len(compactJSON)/4

	if compactPrompt != wantCompactPrompt {
		t.Fatalf("compact prompt estimate = %d, want transmitted-schema estimate %d", compactPrompt, wantCompactPrompt)
	}
	if compactPrompt >= fullPrompt {
		t.Fatalf("compact prompt estimate %d was not smaller than full-schema estimate %d", compactPrompt, fullPrompt)
	}
	if compactCompletion != 0 {
		t.Fatalf("compact completion estimate = %d, want 0", compactCompletion)
	}
	if !compactEstimated || !fullEstimated {
		t.Fatalf("preflight usage must be estimated, got compact=%t full=%t", compactEstimated, fullEstimated)
	}
}

func TestGetGlobalTokenUsageTreatsProviderMetadataAsMeasured(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hi"},
		{
			Role:             "assistant",
			Content:          "hello",
			PromptTokens:     1600,
			CompletionTokens: 19,
		},
	}

	prompt, completion, estimated := a.GetGlobalTokenUsage(messages, nil)
	if prompt != 1600 || completion != 19 {
		t.Fatalf("provider usage = (%d, %d), want (1600, 19)", prompt, completion)
	}
	if estimated {
		t.Fatal("provider-reported usage was incorrectly marked as estimated")
	}

	messages = append(messages, db.Message{Role: "user", Content: "12345678"})
	prompt, completion, estimated = a.GetGlobalTokenUsage(messages, nil)
	if prompt != 1602 || completion != 19 {
		t.Fatalf("usage with pending input = (%d, %d), want (1602, 19)", prompt, completion)
	}
	if !estimated {
		t.Fatal("usage with locally counted pending input was not marked as estimated")
	}
}
