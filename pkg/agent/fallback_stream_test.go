package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maquis/pkg/config"
	"maquis/pkg/db"
)

func filterFallbackChunks(chunks ...string) string {
	var output strings.Builder
	filter := newFallbackToolTextFilter(func(text string) {
		output.WriteString(text)
	})
	for _, chunk := range chunks {
		filter.Write(chunk)
	}
	filter.Flush()
	return output.String()
}

func TestFallbackToolTextFilterHandlesEveryChunkBoundary(t *testing.T) {
	input := "Before\n" +
		`<tool_call name="read">{"path":"a.py"}</tool_call>` + "\n" +
		`<tool:read>{"path":"b.py"}</tool:read>` + "\n" +
		`<execute name="read">{"path":"c.py"}</execute>` + "\n" +
		"After"
	const expected = "Before\nAfter"

	for split := 0; split <= len(input); split++ {
		got := filterFallbackChunks(input[:split], input[split:])
		if got != expected {
			t.Fatalf("split %d produced %q, want %q", split, got, expected)
		}
	}

	byteChunks := make([]string, 0, len(input))
	for i := range len(input) {
		byteChunks = append(byteChunks, input[i:i+1])
	}
	if got := filterFallbackChunks(byteChunks...); got != expected {
		t.Fatalf("byte-at-a-time filtering produced %q, want %q", got, expected)
	}
}

func TestFallbackToolTextFilterPreservesOrdinaryMarkup(t *testing.T) {
	input := "Use <toolbox>, <tool_calligraphy>, and <executeLater> literally."
	if got := filterFallbackChunks(input); got != input {
		t.Fatalf("ordinary markup changed from %q to %q", input, got)
	}
}

func TestStripFallbackToolMarkup(t *testing.T) {
	input := `Text <tool_call name="read">{"path":"secret.py"}</tool_call> after`
	if got := StripFallbackToolMarkup(input); got != "Text after" {
		t.Fatalf("unexpected stripped content: %q", got)
	}
}

func TestFallbackToolMarkupIsHiddenFromStreamContentAndNextRequest(t *testing.T) {
	var capturedRequest ChatCompletionRequest
	responseChunks := []string{
		"First, I will read.\n\n<tool_",
		`call name="read">{"path":"a.py"}</tool_call>` + "\n",
		`<tool_call name="read">{"path":"b.py"}</tool_call>`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedRequest); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		for _, content := range responseChunks {
			payload, err := json.Marshal(map[string]any{
				"choices": []any{
					map[string]any{
						"delta": map[string]any{"content": content},
					},
				},
			})
			if err != nil {
				t.Errorf("marshal response chunk: %v", err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	legacyMarkup := `<tool_call name="read">{"path":"legacy.py"}</tool_call>`
	legacyCalls := ParseFallbackToolCalls(legacyMarkup)
	provider := &OpenAICompatibleProvider{
		Config: &config.Config{
			Endpoint:           server.URL,
			Model:              "test",
			ContextWindowLimit: 128000,
		},
		HttpClient:             server.Client(),
		ThinkingSupportChecked: true,
	}
	chunks := make(chan StreamChunk, 32)
	message, err := provider.StreamChatCompletions(
		context.Background(),
		[]db.Message{
			{Role: "user", Content: "read files"},
			{Role: "assistant", Content: legacyMarkup, ToolCalls: legacyCalls},
		},
		nil,
		chunks,
	)
	if err != nil {
		t.Fatalf("stream completion: %v", err)
	}

	var visible strings.Builder
	for len(chunks) > 0 {
		chunk := <-chunks
		if chunk.Type == "text" {
			visible.WriteString(chunk.Content)
		}
	}

	for label, content := range map[string]string{
		"visible stream": visible.String(),
		"saved content":  message.Content,
	} {
		if strings.Contains(content, "<tool_call") || strings.Contains(content, `"path"`) {
			t.Fatalf("%s leaked fallback markup: %q", label, content)
		}
	}
	if len(message.ToolCalls) != 2 {
		t.Fatalf("expected two parsed fallback calls, got %d", len(message.ToolCalls))
	}

	for _, requestMessage := range capturedRequest.Messages {
		if strings.Contains(requestMessage.Content, "<tool_call") {
			t.Fatalf("next request retained legacy fallback markup: %q", requestMessage.Content)
		}
	}
}
