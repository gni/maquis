package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/db"
)

func TestDebugLoggerLogsExecutionTracesAndRereads(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "debug.log")

	logger := NewDebugLogger(tmpDir, logPath)
	if logger == nil {
		t.Fatal("expected non-nil DebugLogger")
	}
	defer logger.Close()

	if logger.FilePath() != logPath {
		t.Fatalf("expected filePath %s, got %s", logPath, logger.FilePath())
	}

	// 1. Log user command
	logger.LogUserCommand("session-123", "inspect and modify code")

	// 2. Log LLM request
	msgs := []db.Message{
		{Role: "system", Content: "You are maquis."},
		{Role: "user", Content: "inspect and modify code"},
	}
	tools := []tool.Tool{
		tool.NewReadTool().Definition(),
	}
	logger.LogLLMRequest("session-123", 1, "test-model", "http://localhost:8080", msgs, tools)

	// 3. Log LLM response with thought and tool call
	assistantMsg := &db.Message{
		Role:             "assistant",
		ReasoningContent: "I need to check the code in main.go first.",
		ToolCalls: []db.ToolCall{
			{
				ID: "call_abc123",
				Function: db.ToolFunction{
					Name:      "read",
					Arguments: `{"path":"main.go","offset":1,"limit":50}`,
				},
			},
		},
	}
	logger.LogLLMResponse("session-123", 1, assistantMsg, 250*time.Millisecond)

	// 4. Log tool execution (first time)
	logger.LogToolExecution("session-123", 1, "read", `{"path":"main.go","offset":1,"limit":50}`, "package main\n\nfunc main() {}\n", nil, 5*time.Millisecond)

	// 5. Log tool execution (second time on same file -> re-read detected)
	logger.LogToolExecution("session-123", 2, "read", `{"path":"main.go","offset":1,"limit":50}`, "package main\n\nfunc main() {}\n", nil, 4*time.Millisecond)

	// 6. Log error
	logger.LogToolExecution("session-123", 3, "bash", `{"command":"nonexistent_cmd"}`, "", errors.New("command not found"), 12*time.Millisecond)

	// 7. Log repetition
	logger.LogRepetition("session-123", "read", `{"path":"main.go"}`, 4, "repetition breaker reached")

	logger.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read debug log: %v", err)
	}
	content := string(data)

	// Verify all elements are captured
	expectedSubstrings := []string{
		"USER COMMAND",
		"inspect and modify code",
		"LLM REQUEST",
		"test-model",
		"LLM RESPONSE",
		"I need to check the code in main.go first",
		"TOOL EXECUTION: read",
		"package main",
		"[RE-READ DETECTED #2]",
		"Target file 'main.go' has been read 2 times this turn",
		"TOOL EXECUTION: bash",
		"command not found",
		"REPETITION WARNING",
	}

	for _, expected := range expectedSubstrings {
		if !strings.Contains(content, expected) {
			t.Errorf("debug log missing expected substring: %q", expected)
		}
	}
}

func TestDebugLoggerDisabled(t *testing.T) {
	tmpDir := t.TempDir()

	disabledValues := []string{"off", "OFF", "none", "false", "disabled", "/dev/null", "null"}
	for _, val := range disabledValues {
		logger := NewDebugLogger(tmpDir, val)
		if logger != nil {
			t.Errorf("expected NewDebugLogger with %q to return nil, got non-nil", val)
		}
	}
}

