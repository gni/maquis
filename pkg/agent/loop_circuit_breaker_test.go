package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

type repeatToolProvider struct {
	toolName string
	toolArgs string
	calls    atomic.Int32
}

func (p *repeatToolProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *repeatToolProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	callNum := p.calls.Add(1)
	if callNum > 4 {
		return &db.Message{
			Role:    "assistant",
			Content: "stopped repeating",
		}, nil
	}
	return &db.Message{
		Role: "assistant",
		ToolCalls: []db.ToolCall{
			{
				ID: fmt.Sprintf("call_repeat_%d", callNum),
				Function: db.ToolFunction{
					Name:      p.toolName,
					Arguments: p.toolArgs,
				},
			},
		},
	}, nil
}

func TestIdenticalToolCallLoopBreaker(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(filePath, []byte("sample content\n"), 0644)

	provider := &repeatToolProvider{
		toolName: "read",
		toolArgs: `{"path":"test.txt"}`,
	}

	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool())

	a := &Agent{
		WorkspaceRoot: tmpDir,
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    30,
			AutoApprove:          true,
		},
		LLMProvider: provider,
		Registry:    registry,
	}

	messages := []db.Message{
		{Role: "system", Content: "system"},
	}

	var buf bytes.Buffer
	a.RunAgentLoop(context.Background(), &buf, &messages, "read test.txt", nil, style.UITheme{}, true, "")

	foundLoopError := false
	for _, m := range messages {
		if m.Role == "tool" && strings.Contains(m.Content, "loop detected") {
			foundLoopError = true
			break
		}
	}

	if !foundLoopError {
		t.Fatalf("expected loop detected circuit breaker message in tool output, got messages: %+v", messages)
	}
}

type sequenceSearchProvider struct {
	calls atomic.Int32
}

func (p *sequenceSearchProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *sequenceSearchProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	callNum := p.calls.Add(1)
	if callNum > 16 {
		return &db.Message{
			Role:    "assistant",
			Content: "done",
		}, nil
	}
	// Different query each time so callKey is unique
	return &db.Message{
		Role: "assistant",
		ToolCalls: []db.ToolCall{
			{
				ID: fmt.Sprintf("call_search_%d", callNum),
				Function: db.ToolFunction{
					Name:      "grep",
					Arguments: fmt.Sprintf(`{"pattern":"term_%d"}`, callNum),
				},
			},
		},
	}, nil
}

func TestConsecutiveInspectionNotLimited(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &sequenceSearchProvider{}

	registry := tool.NewToolRegistry()
	registry.Register(tool.NewGrepTool())

	a := &Agent{
		WorkspaceRoot: tmpDir,
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    30,
			AutoApprove:          true,
		},
		LLMProvider: provider,
		Registry:    registry,
	}

	messages := []db.Message{
		{Role: "system", Content: "system"},
	}

	var buf bytes.Buffer
	a.RunAgentLoop(context.Background(), &buf, &messages, "find things", nil, style.UITheme{}, true, "")

	for _, m := range messages {
		if m.Role == "tool" {
			if strings.Contains(m.Content, "Inspection limit reached") || strings.Contains(m.Content, "consecutive read/search") {
				t.Fatalf("unexpected inspection limit warning/error in tool output: %s", m.Content)
			}
		}
	}
}

type sequenceBashProvider struct {
	calls atomic.Int32
}

func (p *sequenceBashProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *sequenceBashProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	callNum := p.calls.Add(1)
	if callNum > 12 {
		return &db.Message{
			Role:    "assistant",
			Content: "done executing commands",
		}, nil
	}
	return &db.Message{
		Role: "assistant",
		ToolCalls: []db.ToolCall{
			{
				ID: fmt.Sprintf("call_bash_%d", callNum),
				Function: db.ToolFunction{
					Name:      "bash",
					Arguments: fmt.Sprintf(`{"command":"echo step_%d"}`, callNum),
				},
			},
		},
	}, nil
}

func TestBashCommandsDoNotTriggerInspectionWarning(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &sequenceBashProvider{}

	registry := tool.NewToolRegistry()
	registry.Register(tool.NewBashTool())

	a := &Agent{
		WorkspaceRoot: tmpDir,
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    30,
			AutoApprove:          true,
		},
		LLMProvider: provider,
		Registry:    registry,
	}

	messages := []db.Message{
		{Role: "system", Content: "system"},
	}

	var buf bytes.Buffer
	a.RunAgentLoop(context.Background(), &buf, &messages, "run commands", nil, style.UITheme{}, true, "")

	for _, m := range messages {
		if m.Role == "tool" {
			if strings.Contains(m.Content, "consecutive read/search") || strings.Contains(m.Content, "Inspection limit") {
				t.Fatalf("bash command should never trigger inspection warning, got: %s", m.Content)
			}
		}
	}
}

func TestToolCategoryClassification(t *testing.T) {
	if !IsInspectionTool("read") || !IsInspectionTool("grep") || !IsInspectionTool("find") || !IsInspectionTool("list") {
		t.Fatal("inspection tools misclassified")
	}
	if IsInspectionTool("bash") || IsInspectionTool("write") || IsInspectionTool("edit") || IsInspectionTool("task_kill") {
		t.Fatal("action tools misclassified as inspection tools")
	}

	if !IsActionTool("bash") || !IsActionTool("write") || !IsActionTool("edit") || !IsActionTool("task_kill") {
		t.Fatal("action tools misclassified")
	}
	if !IsActionTool("subagent__critic") || !IsActionTool("spawn_subagent") {
		t.Fatal("subagents should be classified as action tools")
	}
	if IsActionTool("read") || IsActionTool("grep") || IsActionTool("find") || IsActionTool("list") {
		t.Fatal("inspection tools misclassified as action tools")
	}
}

func TestAlternatingReadsOfSameFileTriggerLoopBreaker(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "file_a.txt")
	fileB := filepath.Join(tmpDir, "file_b.txt")
	_ = os.WriteFile(fileA, []byte("content A\n"), 0644)
	_ = os.WriteFile(fileB, []byte("content B\n"), 0644)

	provider := &alternatingReadProvider{}

	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool())

	a := &Agent{
		WorkspaceRoot: tmpDir,
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    30,
			AutoApprove:          true,
		},
		LLMProvider: provider,
		Registry:    registry,
	}

	messages := []db.Message{
		{Role: "system", Content: "system"},
	}

	var buf bytes.Buffer
	a.RunAgentLoop(context.Background(), &buf, &messages, "read files alternating", nil, style.UITheme{}, true, "")

	foundLoopError := false
	for _, m := range messages {
		if m.Role == "tool" && strings.Contains(m.Content, "loop detected") && strings.Contains(m.Content, "has already been inspected 3 times") {
			foundLoopError = true
			break
		}
	}
	if !foundLoopError {
		t.Fatalf("expected alternating re-reads of the same file to trigger loop detection, got messages: %+v", messages)
	}
}

type alternatingReadProvider struct {
	calls atomic.Int32
}

func (p *alternatingReadProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *alternatingReadProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	callNum := p.calls.Add(1)
	if callNum > 8 {
		return &db.Message{
			Role:    "assistant",
			Content: "finished alternating reads",
		}, nil
	}
	target := "file_a.txt"
	if callNum%2 == 0 {
		target = "file_b.txt"
	}
	return &db.Message{
		Role: "assistant",
		ToolCalls: []db.ToolCall{
			{
				ID: fmt.Sprintf("call_alt_%d", callNum),
				Function: db.ToolFunction{
					Name:      "read",
					Arguments: fmt.Sprintf(`{"path":"%s"}`, target),
				},
			},
		},
	}, nil
}

