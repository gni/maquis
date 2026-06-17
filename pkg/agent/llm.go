package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/db"
)

type Tool = tool.Tool
type FunctionDefinition = tool.FunctionDefinition
type JSONSchema = tool.JSONSchema
type SchemaProp = tool.SchemaProp

type StreamChunk struct {
	Type          string
	Content       string
	ToolCallIndex int
}

type ReplaceEdit = tool.ReplaceEdit

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
}

type ChatCompletionRequest struct {
	Model                string              `json:"model"`
	Messages             []db.Message        `json:"messages"`
	Tools                []Tool              `json:"tools,omitempty"`
	Temperature          float64             `json:"temperature"`
	Stream               bool                `json:"stream"`
	StreamOptions        *StreamOptions      `json:"stream_options,omitempty"`
	ReasoningEffort      string              `json:"reasoning_effort,omitempty"`
	ReasoningFormat      string              `json:"reasoning_format,omitempty"`
	ThinkingBudgetTokens int                 `json:"thinking_budget_tokens,omitempty"`
	ReasoningControl     bool                `json:"reasoning_control,omitempty"`
	ChatTemplateKwargs   *ChatTemplateKwargs `json:"chat_template_kwargs,omitempty"`
}

type ChatCompletionResponseChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"`
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

func (a *Agent) CheckThinkingSupport() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/props?model=%s", strings.TrimSuffix(a.Config.Endpoint, "/"), a.Config.Model)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	if a.Config.ApiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.Config.ApiKey))
	}
	resp, err := a.HttpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (a *Agent) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(a.Config.Endpoint, "/"))

	if !a.ThinkingSupportChecked {
		a.ThinkingSupported = a.CheckThinkingSupport()
		a.ThinkingSupportChecked = true
	}

	enableThinking := a.Config.ShowThinking
	budget := -1
	if enableThinking {
		switch strings.ToLower(a.Config.ReasoningEffort) {
		case "low":
			budget = 512
		case "medium":
			budget = 2048
		case "high":
			budget = 8192
		case "max":
			budget = -1
		default:
			budget = 512
		}
	}

	var apiMessages []db.Message
	for _, msg := range messages {
		if msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) == 0 {
			// Skip completely empty assistant messages to avoid API rejection
			continue
		}
		if msg.Role != "assistant" && msg.Content == "" {
			msgCopy := msg
			msgCopy.Content = " "
			apiMessages = append(apiMessages, msgCopy)
		} else {
			apiMessages = append(apiMessages, msg)
		}
	}

	reqBody := ChatCompletionRequest{
		Model:       a.Config.Model,
		Messages:    apiMessages,
		Tools:       a.Registry.GetAvailableTools(allowlist),
		Temperature: a.Config.Temperature,
		Stream:      true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
		ReasoningEffort: a.Config.ReasoningEffort,
	}

	if a.ThinkingSupported {
		reqBody.ReasoningControl = true
		if enableThinking {
			reqBody.ReasoningFormat = "auto"
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking: true,
			}
			if budget >= 0 {
				reqBody.ThinkingBudgetTokens = budget
			}
		} else {
			reqBody.ReasoningFormat = "none"
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking: false,
			}
		}
	}

	if len(reqBody.Tools) == 0 {
		reqBody.Tools = nil
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	var resp *http.Response
	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		if a.Config.ApiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.Config.ApiKey))
		}

		var doErr error
		resp, doErr = a.HttpClient.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", doErr, a.Config.Endpoint)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server returned non-200 status: %d. Body: %s", resp.StatusCode, string(body))

			if resp.StatusCode >= 500 {
				continue
			}
			return nil, lastErr
		}

		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCallsMap = make(map[int]*db.ToolCall)

	var promptTokens, completionTokens int
	var generationStart time.Time

	inThoughtMode := false
	streamBuffer := ""

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

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

		if choice.Delta.ReasoningContent != "" || choice.Delta.Content != "" {
			if generationStart.IsZero() {
				generationStart = time.Now()
			}
		}

		if choice.Delta.ReasoningContent != "" {
			reasoningBuilder.WriteString(choice.Delta.ReasoningContent)
			chunkChan <- StreamChunk{Type: "reasoning", Content: choice.Delta.ReasoningContent}
		}

		if choice.Delta.Content != "" {
			streamBuffer += choice.Delta.Content
			for {
				if !inThoughtMode {
					tag := "<|channel>thought"
					idx := strings.Index(streamBuffer, tag)
					if idx != -1 {
						preText := streamBuffer[:idx]
						if preText != "" {
							textBuilder.WriteString(preText)
							chunkChan <- StreamChunk{Type: "text", Content: preText}
						}
						streamBuffer = streamBuffer[idx+len(tag):]
						inThoughtMode = true
						continue
					}
					var prefixMatched int
					for i := len(tag) - 1; i >= 1; i-- {
						if strings.HasSuffix(streamBuffer, tag[:i]) {
							prefixMatched = i
							break
						}
					}
					if prefixMatched > 0 {
						sendLen := len(streamBuffer) - prefixMatched
						if sendLen > 0 {
							preText := streamBuffer[:sendLen]
							textBuilder.WriteString(preText)
							chunkChan <- StreamChunk{Type: "text", Content: preText}
							streamBuffer = streamBuffer[sendLen:]
						}
						break
					}
					textBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "text", Content: streamBuffer}
					streamBuffer = ""
					break
				} else {
					tag := "<channel|>"
					idx := strings.Index(streamBuffer, tag)
					if idx != -1 {
						preReasoning := streamBuffer[:idx]
						if preReasoning != "" {
							reasoningBuilder.WriteString(preReasoning)
							chunkChan <- StreamChunk{Type: "reasoning", Content: preReasoning}
						}
						streamBuffer = streamBuffer[idx+len(tag):]
						inThoughtMode = false
						continue
					}
					var prefixMatched int
					for i := len(tag) - 1; i >= 1; i-- {
						if strings.HasSuffix(streamBuffer, tag[:i]) {
							prefixMatched = i
							break
						}
					}
					if prefixMatched > 0 {
						sendLen := len(streamBuffer) - prefixMatched
						if sendLen > 0 {
							preReasoning := streamBuffer[:sendLen]
							reasoningBuilder.WriteString(preReasoning)
							chunkChan <- StreamChunk{Type: "reasoning", Content: preReasoning}
							streamBuffer = streamBuffer[sendLen:]
						}
						break
					}
					reasoningBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "reasoning", Content: streamBuffer}
					streamBuffer = ""
					break
				}
			}
		}

		if len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}

				existing, ok := toolCallsMap[idx]
				if !ok {
					newTC := tc
					if newTC.ID == "" {
						newTC.ID = fmt.Sprintf("call_%d_%s", idx, db.NewUUID()[:8])
					}
					toolCallsMap[idx] = &newTC
					if tc.Function.Name != "" {
						chunkChan <- StreamChunk{Type: "tool_name", Content: tc.Function.Name, ToolCallIndex: idx}
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
						chunkChan <- StreamChunk{Type: "tool_name", Content: tc.Function.Name, ToolCallIndex: idx}
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.Function.Arguments != "" {
					chunkChan <- StreamChunk{Type: "tool_call", Content: tc.Function.Arguments, ToolCallIndex: idx}
				}
			}
		}
	}

	if streamBuffer != "" {
		if inThoughtMode {
			reasoningBuilder.WriteString(streamBuffer)
			chunkChan <- StreamChunk{Type: "reasoning", Content: streamBuffer}
		} else {
			textBuilder.WriteString(streamBuffer)
			chunkChan <- StreamChunk{Type: "text", Content: streamBuffer}
		}
	}

	if !generationStart.IsZero() {
		a.lastGenerationDuration = time.Since(generationStart)
	} else {
		a.lastGenerationDuration = 0
	}

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
		maxIdx := -1
		for idx := range toolCallsMap {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if tc, ok := toolCallsMap[i]; ok {
				assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, *tc)
			}
		}
	}

	return assistantMsg, nil
}