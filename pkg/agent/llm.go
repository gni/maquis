package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
)

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type JSONSchema struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

func (j JSONSchema) MarshalJSON() ([]byte, error) {
	var propsParts []string

	orderedKeys := []string{"path", "command"}
	for _, key := range orderedKeys {
		if prop, ok := j.Properties[key]; ok {
			propBytes, err := json.Marshal(prop)
			if err != nil {
				return nil, err
			}
			propsParts = append(propsParts, fmt.Sprintf("%q:%s", key, string(propBytes)))
		}
	}

	var otherKeys []string
	for k := range j.Properties {
		isOrdered := false
		for _, ok := range orderedKeys {
			if k == ok {
				isOrdered = true
				break
			}
		}
		if !isOrdered {
			otherKeys = append(otherKeys, k)
		}
	}
	sort.Strings(otherKeys)

	for _, k := range otherKeys {
		propBytes, err := json.Marshal(j.Properties[k])
		if err != nil {
			return nil, err
		}
		propsParts = append(propsParts, fmt.Sprintf("%q:%s", k, string(propBytes)))
	}

	propsJSON := "{" + strings.Join(propsParts, ",") + "}"

	var requiredJSON string
	if len(j.Required) > 0 {
		reqBytes, err := json.Marshal(j.Required)
		if err != nil {
			return nil, err
		}
		requiredJSON = fmt.Sprintf(",%q:%s", "required", string(reqBytes))
	}

	fullJSON := fmt.Sprintf("{%q:%q,%q:%s%s}", "type", j.Type, "properties", propsJSON, requiredJSON)
	return []byte(fullJSON), nil
}

type SchemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type StreamChunk struct {
	Type    string // "text" or "reasoning"
	Content string
}

type ReplaceEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatCompletionRequest struct {
	Model           string         `json:"model"`
	Messages        []db.Message   `json:"messages"`
	Tools           []Tool         `json:"tools,omitempty"`
	Temperature     float64        `json:"temperature"`
	Stream          bool           `json:"stream"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

type ChatCompletionResponseChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int `json:"index"`
		Delta        struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"` // Support reasoning step
			ToolCalls        []db.ToolCall `json:"tool_calls"`
			Role             string        `json:"role"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}





func StreamChatCompletions(
	ctx context.Context,
	cfg *config.Config,
	client *http.Client,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(cfg.Endpoint, "/"))

	reqBody := ChatCompletionRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Tools:       GetAvailableTools(allowlist),
		Temperature: cfg.Temperature,
		Stream:      true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
		ReasoningEffort: cfg.ReasoningEffort,
	}

	if len(reqBody.Tools) == 0 {
		reqBody.Tools = nil
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	if cfg.ApiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.ApiKey))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", err, cfg.Endpoint)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned non-200 status: %d. Body: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCallsMap = make(map[int]*db.ToolCall)

	var promptTokens, completionTokens int

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk ChatCompletionResponseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// 1. Process reasoning/thinking delta
		if choice.Delta.ReasoningContent != "" {
			reasoningBuilder.WriteString(choice.Delta.ReasoningContent)
			chunkChan <- StreamChunk{Type: "reasoning", Content: choice.Delta.ReasoningContent}
		}

		// 2. Process regular content delta
		if choice.Delta.Content != "" {
			textBuilder.WriteString(choice.Delta.Content)
			chunkChan <- StreamChunk{Type: "text", Content: choice.Delta.Content}
		}

		// 3. Process tool calls
		if len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}

				existing, ok := toolCallsMap[idx]
				if !ok {
					newTC := tc
					toolCallsMap[idx] = &newTC
					if tc.Function.Name != "" {
						chunkChan <- StreamChunk{Type: "tool_name", Content: tc.Function.Name}
					}
				} else {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Type != "" {
						existing.Type = tc.Type
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
						chunkChan <- StreamChunk{Type: "tool_name", Content: tc.Function.Name}
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.Function.Arguments != "" {
					chunkChan <- StreamChunk{Type: "tool_call", Content: tc.Function.Arguments}
				}
			}
		}
	}

	// Fallback token estimation if not provided by stream metadata
	if promptTokens == 0 {
		totalChars := 0
		for _, msg := range messages {
			totalChars += len(msg.Content) + len(msg.ReasoningContent)
		}
		promptTokens = totalChars / 4
		if promptTokens == 0 && totalChars > 0 {
			promptTokens = 1
		}
	}
	if completionTokens == 0 {
		completionTokens = (len(textBuilder.String()) + len(reasoningBuilder.String())) / 4
		if completionTokens == 0 && (len(textBuilder.String()) + len(reasoningBuilder.String())) > 0 {
			completionTokens = 1
		}
	}

	assistantMsg := &db.Message{
		Role:             "assistant",
		Content:          textBuilder.String(),
		ReasoningContent: reasoningBuilder.String(),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	}

	if len(toolCallsMap) > 0 {
		for i := 0; i < len(toolCallsMap); i++ {
			if tc, ok := toolCallsMap[i]; ok {
				assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, *tc)
			}
		}
	}

	return assistantMsg, nil
}




