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
	"maquis/pkg/config"
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
	MaxCompletionTokens  int                 `json:"max_completion_tokens,omitempty"`
	MaxTokens            int                 `json:"max_tokens,omitempty"`
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

type LLMProvider interface {
	StreamChatCompletions(
		ctx context.Context,
		messages []db.Message,
		tools []tool.Tool,
		chunkChan chan<- StreamChunk,
	) (*db.Message, error)
	CheckThinkingSupport(ctx context.Context) bool
}

type OpenAICompatibleProvider struct {
	Config                 *config.Config
	HttpClient             *http.Client
	ThinkingSupported      bool
	ThinkingSupportChecked bool
}

func (p *OpenAICompatibleProvider) CheckThinkingSupport(ctx context.Context) bool {
	url := fmt.Sprintf("%s/props?model=%s", strings.TrimSuffix(p.Config.Endpoint, "/"), p.Config.Model)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("maquis", "v1.0.0")
	if p.Config.ApiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
	}
	resp, err := p.HttpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *OpenAICompatibleProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(p.Config.Endpoint, "/"))

	if !p.ThinkingSupportChecked {
		p.ThinkingSupported = p.CheckThinkingSupport(ctx)
		p.ThinkingSupportChecked = true
	}

	enableThinking := p.Config.ShowThinking
	budget := -1
	if enableThinking {
		switch strings.ToLower(p.Config.ReasoningEffort) {
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

	var validMessages []db.Message
	limit := p.Config.ContextWindowLimit
	if limit <= 0 {
		limit = 128000
	}
	limitChars := limit * 4
	totalChars := 0

	for _, msg := range messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var validTCs []db.ToolCall
			for _, tc := range msg.ToolCalls {
				var dummy map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &dummy); err == nil {
					validTCs = append(validTCs, tc)
				}
			}
			msg.ToolCalls = validTCs
		}
		if msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		if msg.Role != "assistant" && msg.Content == "" {
			msgCopy := msg
			msgCopy.Content = " "
			validMessages = append(validMessages, msgCopy)
		} else {
			validMessages = append(validMessages, msg)
		}
		totalChars += len(validMessages[len(validMessages)-1].Content)
		if validMessages[len(validMessages)-1].Role == "assistant" {
			totalChars += len(validMessages[len(validMessages)-1].ReasoningContent)
		}
	}

	startIndex := 0
	if len(validMessages) > 0 && validMessages[0].Role == "system" {
		startIndex = 1
	}
	for totalChars > limitChars && startIndex < len(validMessages)-1 {
		dropMsg := validMessages[startIndex]
		dropChars := len(dropMsg.Content)
		if dropMsg.Role == "assistant" {
			dropChars += len(dropMsg.ReasoningContent)
		}
		totalChars -= dropChars
		startIndex++
	}

	var apiMessages []db.Message
	if startIndex > 0 && len(validMessages) > 0 && validMessages[0].Role == "system" {
		apiMessages = append(apiMessages, validMessages[0])
		if startIndex < len(validMessages) {
			apiMessages = append(apiMessages, validMessages[startIndex:]...)
		}
	} else if startIndex > 0 && len(validMessages) > 0 {
		apiMessages = validMessages[startIndex:]
	} else {
		apiMessages = validMessages
	}

	var finalTools []tool.Tool
	if p.Config.CompactPrompt {
		for _, t := range tools {
			finalTools = append(finalTools, compressToolDefinition(t))
		}
	} else {
		finalTools = tools
	}

	reqBody := ChatCompletionRequest{
		Model:       p.Config.Model,
		Messages:    apiMessages,
		Tools:       finalTools,
		Temperature: p.Config.Temperature,
		Stream:      true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
		ReasoningEffort:     p.Config.ReasoningEffort,
		MaxCompletionTokens: p.Config.MaxCompletionTokens,
		MaxTokens:           p.Config.MaxCompletionTokens,
	}

	if p.ThinkingSupported {
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
		req.Header.Set("maquis", "v1.0.0")

		if p.Config.ApiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
		}

		var doErr error
		resp, doErr = p.HttpClient.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", doErr, p.Config.Endpoint)
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
	var chunk ChatCompletionResponseChunk

	for {
		select {
		case <-ctx.Done():
			// Build partial message before returning
			if streamBuffer != "" {
				if inThoughtMode {
					reasoningBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "reasoning", Content: streamBuffer}
				} else {
					textBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "text", Content: streamBuffer}
				}
				streamBuffer = ""
			}
			
			if promptTokens == 0 {
				promptTokens = 1 // Prevent div by zero
			}
			if completionTokens == 0 {
				completionTokens = (len(textBuilder.String()) + len(reasoningBuilder.String())) / 4
				if completionTokens == 0 {
					completionTokens = 1
				}
			}

			partialMsg := &db.Message{
				Role:             "assistant",
				Content:          textBuilder.String(),
				ReasoningContent: reasoningBuilder.String(),
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
			}
			
			// Append tool calls to partial msg
			if len(toolCallsMap) > 0 {
				maxIdx := -1
				for idx := range toolCallsMap {
					if idx > maxIdx {
						maxIdx = idx
					}
				}
				for i := 0; i <= maxIdx; i++ {
					if tc, ok := toolCallsMap[i]; ok {
						partialMsg.ToolCalls = append(partialMsg.ToolCalls, *tc)
					}
				}
			} else {
				fallbackCalls := ParseFallbackToolCalls(partialMsg.Content)
				if len(fallbackCalls) > 0 {
					partialMsg.ToolCalls = fallbackCalls
				}
			}

			return partialMsg, ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				// Context cancelled while reading, return partial message
				if streamBuffer != "" {
					if inThoughtMode {
						reasoningBuilder.WriteString(streamBuffer)
						chunkChan <- StreamChunk{Type: "reasoning", Content: streamBuffer}
					} else {
						textBuilder.WriteString(streamBuffer)
						chunkChan <- StreamChunk{Type: "text", Content: streamBuffer}
					}
					streamBuffer = ""
				}
				
				if promptTokens == 0 { promptTokens = 1 }
				if completionTokens == 0 {
					completionTokens = (len(textBuilder.String()) + len(reasoningBuilder.String())) / 4
					if completionTokens == 0 { completionTokens = 1 }
				}

				partialMsg := &db.Message{
					Role:             "assistant",
					Content:          textBuilder.String(),
					ReasoningContent: reasoningBuilder.String(),
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
				}
				
				if len(toolCallsMap) > 0 {
					maxIdx := -1
					for idx := range toolCallsMap {
						if idx > maxIdx { maxIdx = idx }
					}
					for i := 0; i <= maxIdx; i++ {
						if tc, ok := toolCallsMap[i]; ok {
							partialMsg.ToolCalls = append(partialMsg.ToolCalls, *tc)
						}
					}
				} else {
					fallbackCalls := ParseFallbackToolCalls(partialMsg.Content)
					if len(fallbackCalls) > 0 {
						partialMsg.ToolCalls = fallbackCalls
					}
				}
				return partialMsg, ctx.Err()
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

		chunk = ChatCompletionResponseChunk{}
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
			if streamBuffer != "" {
				if inThoughtMode {
					reasoningBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "reasoning", Content: streamBuffer}
				} else {
					textBuilder.WriteString(streamBuffer)
					chunkChan <- StreamChunk{Type: "text", Content: streamBuffer}
				}
				streamBuffer = ""
			}

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

	var duration time.Duration
	if !generationStart.IsZero() {
		duration = time.Since(generationStart)
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
	} else {
		// Fallback parser: extract tool calls from text content if native tool calls are empty
		fallbackCalls := ParseFallbackToolCalls(assistantMsg.Content)
		if len(fallbackCalls) > 0 {
			assistantMsg.ToolCalls = fallbackCalls
		}
	}

	// Store duration in context/metadata or handle via caller setting
	ctxVal := ctx.Value("generation_duration_callback")
	if callback, ok := ctxVal.(func(time.Duration)); ok {
		callback(duration)
	}

	return assistantMsg, nil
}

// Delegators on Agent struct to maintain backwards compatibility

func (a *Agent) CheckThinkingSupport() bool {
	if a.LLMProvider == nil {
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     a.Config,
			HttpClient: a.HttpClient,
		}
	} else {
		if oai, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok {
			oai.Config = a.Config
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.LLMProvider.CheckThinkingSupport(ctx)
}

func (a *Agent) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	if a.LLMProvider == nil {
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     a.Config,
			HttpClient: a.HttpClient,
		}
	} else {
		if oai, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok {
			if oai.Config != a.Config || (oai.Config != nil && (oai.Config.Endpoint != a.Config.Endpoint || oai.Config.Model != a.Config.Model)) {
				oai.ThinkingSupportChecked = false
				oai.ThinkingSupported = false
			}
			oai.Config = a.Config
		}
	}

	// Capture generation duration via context value
	durationChan := make(chan time.Duration, 1)
	ctxWithCallback := context.WithValue(ctx, "generation_duration_callback", func(d time.Duration) {
		select {
		case durationChan <- d:
		default:
		}
	})

	tools := a.Registry.GetAvailableTools(allowlist)
	msg, err := a.LLMProvider.StreamChatCompletions(ctxWithCallback, messages, tools, chunkChan)

	select {
	case d := <-durationChan:
		a.lastGenerationDuration = d
	default:
		a.lastGenerationDuration = 0
	}

	return msg, err
}

func compressToolDefinition(t tool.Tool) tool.Tool {
	compressed := t
	newProps := make(map[string]tool.SchemaProp)
	for k, v := range t.Function.Parameters.Properties {
		newProps[k] = v
	}
	compressed.Function.Parameters.Properties = newProps

	switch t.Function.Name {
	case "ls":
		compressed.Function.Description = "Run bash command"
		if prop, ok := compressed.Function.Parameters.Properties["command"]; ok {
			prop.Description = "Command string"
			compressed.Function.Parameters.Properties["command"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["background"]; ok {
			prop.Description = "Run in background"
			compressed.Function.Parameters.Properties["background"] = prop
		}
	case "read":
		compressed.Function.Description = "Read file lines. Read full contents when preparing to edit."
		if compressed.Function.Parameters.Properties != nil {
			if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
				prop.Description = "File path"
				compressed.Function.Parameters.Properties["path"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["offset"]; ok {
				prop.Description = "Start line"
				compressed.Function.Parameters.Properties["offset"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["limit"]; ok {
				prop.Description = "Max lines"
				compressed.Function.Parameters.Properties["limit"] = prop
			}
		}
	case "write":
		compressed.Function.Description = "Write file"
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "File path"
			compressed.Function.Parameters.Properties["path"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["write_content"]; ok {
			prop.Description = "File content"
			compressed.Function.Parameters.Properties["write_content"] = prop
		}
	case "edit":
		compressed.Function.Description = "Edit file blocks"
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "File path"
			compressed.Function.Parameters.Properties["path"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["updates"]; ok {
			prop.Description = "Replacements array"
			if prop.Items != nil {
				itemsCopy := *prop.Items
				itemsProps := make(map[string]tool.SchemaProp)
				for k, v := range itemsCopy.Properties {
					itemsProps[k] = v
				}
				itemsCopy.Properties = itemsProps
				
				if oldTextProp, ok := itemsCopy.Properties["oldText"]; ok {
					oldTextProp.Description = "Target content"
					itemsCopy.Properties["oldText"] = oldTextProp
				}
				if newTextProp, ok := itemsCopy.Properties["newText"]; ok {
					newTextProp.Description = "Replacement content"
					itemsCopy.Properties["newText"] = newTextProp
				}
				prop.Items = &itemsCopy
			}
			compressed.Function.Parameters.Properties["updates"] = prop
		}
	case "load_skill":
		compressed.Function.Description = "Load skill instructions"
		if prop, ok := compressed.Function.Parameters.Properties["name"]; ok {
			prop.Description = "Skill name"
			compressed.Function.Parameters.Properties["name"] = prop
		}
	case "task_status":
		compressed.Function.Description = "Check task status"
		if prop, ok := compressed.Function.Parameters.Properties["task_id"]; ok {
			prop.Description = "Task ID"
			compressed.Function.Parameters.Properties["task_id"] = prop
		}
	case "task_kill":
		compressed.Function.Description = "Kill task"
		if prop, ok := compressed.Function.Parameters.Properties["task_id"]; ok {
			prop.Description = "Task ID"
			compressed.Function.Parameters.Properties["task_id"] = prop
		}
	default:
		if len(compressed.Function.Description) > 30 {
			compressed.Function.Description = compressed.Function.Description[:27] + "..."
		}
		for k, prop := range compressed.Function.Parameters.Properties {
			if len(prop.Description) > 20 {
				prop.Description = prop.Description[:17] + "..."
				compressed.Function.Parameters.Properties[k] = prop
			}
		}
	}
	return compressed
}
