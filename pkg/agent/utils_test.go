package agent

import (
	"errors"
	"strings"
	"testing"

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
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("tool failure omitted %q: %q", expected, formatted)
		}
	}
	if strings.Contains(formatted, "System Alert:") || strings.Contains(formatted, "Recommendation:") {
		t.Fatalf("bash failure should not contain robotic alert slop: %q", formatted)
	}
}

func TestFormatToolExecutionFailureIncludesDefensiveAlertForGenericTools(t *testing.T) {
	formatted := FormatToolExecutionFailure("read", "failed to read file", errors.New("no such file: missing.py"))
	for _, expected := range []string{
		"failed to read file",
		"System Alert:",
		"Recommendation:",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("generic tool omitted expected alert part %q: %q", expected, formatted)
		}
	}
}

func TestFormatToolExecutionFailureDoesNotRepeatGenericFailure(t *testing.T) {
	err := errors.New("command failed: exit status 1 (no output on stdout or stderr)")
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
	if prompt != 160 || completion != 30 {
		t.Fatalf("context with pending user message = (%d, %d); want (160, 30)", prompt, completion)
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
	if prompt != 70000 || completion != 800 {
		t.Fatalf("context anchored to empty cancellation record: got (%d, %d), want (70000, 800)", prompt, completion)
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
	if prompt != 1600 || completion != 19 {
		t.Fatalf("usage with pending input = (%d, %d), want (1600, 19)", prompt, completion)
	}
}

func TestStripEchoedPrompt(t *testing.T) {
	tests := []struct {
		name      string
		reasoning string
		prompt    string
		expected  string
	}{
		{
			name:      "exact prompt with double newline",
			reasoning: "write here a long poem\n\nWe need to write here a long poem.",
			prompt:    "write here a long poem",
			expected:  "We need to write here a long poem.",
		},
		{
			name:      "case-insensitive echoed prompt",
			reasoning: "Write Here A Long Poem\n\nThinking...",
			prompt:    "write here a long poem",
			expected:  "Thinking...",
		},
		{
			name:      "quoted echoed prompt",
			reasoning: "\"write here a long poem\"\n\nThinking...",
			prompt:    "write here a long poem",
			expected:  "Thinking...",
		},
		{
			name:      "prompt with no further reasoning",
			reasoning: "hi\n\n",
			prompt:    "hi",
			expected:  "",
		},
		{
			name:      "distinct reasoning is preserved untouched",
			reasoning: "The user wants a poem about the morning.",
			prompt:    "write here a long poem",
			expected:  "The user wants a poem about the morning.",
		},
		{
			name:      "empty prompt or empty reasoning",
			reasoning: "Some thoughts",
			prompt:    "",
			expected:  "Some thoughts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripEchoedPrompt(tt.reasoning, tt.prompt)
			if result != tt.expected {
				t.Fatalf("StripEchoedPrompt(%q, %q) = %q, want %q", tt.reasoning, tt.prompt, result, tt.expected)
			}
		})
	}
}

