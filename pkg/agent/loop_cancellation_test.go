package agent

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

type cancelledPartialProvider struct {
	started chan struct{}
	calls   atomic.Int32
}

func (p *cancelledPartialProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *cancelledPartialProvider) StreamChatCompletions(
	ctx context.Context,
	_ []db.Message,
	_ []tool.Tool,
	_ chan<- StreamChunk,
) (*db.Message, error) {
	p.calls.Add(1)
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	<-ctx.Done()
	return &db.Message{
		Role:             "assistant",
		PromptTokens:     1,
		CompletionTokens: 1,
	}, ctx.Err()
}

func TestCancelledGenerationDoesNotPersistPartialAssistantOrRestart(t *testing.T) {
	provider := &cancelledPartialProvider{started: make(chan struct{})}
	a := &Agent{
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    30,
		},
		LLMProvider: provider,
		Registry:    tool.NewToolRegistry(),
	}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant", Content: "existing context", PromptTokens: 70000, CompletionTokens: 800},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.RunAgentLoop(ctx, &bytes.Buffer{}, &messages, "continue work", nil, style.UITheme{}, true, "")
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("generation did not start")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled generation did not stop")
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("cancelled generation restarted provider %d times; want once", calls)
	}
	if len(messages) != 3 {
		t.Fatalf("cancelled generation persisted an assistant record: %#v", messages)
	}
	if messages[2].Role != "user" || messages[2].Content != "continue work" {
		t.Fatalf("cancelled generation changed prior context: %#v", messages)
	}
}
