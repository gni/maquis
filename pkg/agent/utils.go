package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

// LoadMemoryContext loads global (~/.maquis/MAQUIS.md) and project (MEMORY.md) memory context.
func (a *Agent) LoadMemoryContext() string {
	var sb strings.Builder

	// 1. Global Memory (~/.maquis/MAQUIS.md)
	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, ".maquis", "MAQUIS.md")
		if data, err := os.ReadFile(globalPath); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if len(trimmed) > 0 {
				sb.WriteString(fmt.Sprintf("\n\nUser Directives (%s):\nAdhere to these global preferences and personal mandates strictly:\n%s", globalPath, trimmed))
			}
		}
	}

	// 2. Project Memory (current workspace MEMORY.md)
	// We search in current directory or traverse up to git root or stop at workspace root
	wd, err := os.Getwd()
	if err == nil {
		dir := wd
		for {
			projectPath := filepath.Join(dir, "MEMORY.md")
			if data, err := os.ReadFile(projectPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\nProject Architecture & Learnings (%s):\nFollow these repository conventions and architectural decisions strictly:\n%s", projectPath, trimmed))
				}
				break
			}

			projectDotPath := filepath.Join(dir, ".maquis", "MEMORY.md")
			if data, err := os.ReadFile(projectDotPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\nProject Architecture & Learnings (%s):\nFollow these repository conventions and architectural decisions strictly:\n%s", projectDotPath, trimmed))
				}
				break
			}

			// Stop if we reach git root, workspace root, or root directory
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			if dir == a.WorkspaceRoot {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return sb.String()
}

// GetGlobalTokens returns the prompt and completion tokens from the latest assistant message.
func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	prompt, completion, _ := a.GetGlobalTokenUsage(messages, allowedTools)
	return prompt, completion
}

// GetGlobalTokenUsage extracts measured token counts returned by the OpenAI API from message history.
func (a *Agent) GetGlobalTokenUsage(messages []db.Message, _ []string) (int, int, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
		if message.Role == "assistant" && hasPayload && (message.PromptTokens > 0 || message.CompletionTokens > 0) {
			return message.PromptTokens, message.CompletionTokens, false
		}
	}
	return 0, 0, false
}

// GetSessionTotalCompletionTokens calculates the global sum of completion tokens generated across all assistant turns in the session.
func (a *Agent) GetSessionTotalCompletionTokens(messages []db.Message) int {
	total := 0
	for _, m := range messages {
		if m.Role == "assistant" {
			hasPayload := m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0
			if hasPayload {
				if m.CompletionTokens > 0 {
					total += m.CompletionTokens
				} else {
					total += (len(m.Content) + len(m.ReasoningContent)) / 4
				}
			}
		}
	}
	return total
}

// GetLatestAssistantCompletionTokens returns the completion tokens of the latest assistant message.
func (a *Agent) GetLatestAssistantCompletionTokens(messages []db.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == "assistant" {
			hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
			if hasPayload && message.CompletionTokens > 0 {
				return message.CompletionTokens
			}
		}
	}
	return 0
}

// FormatDefensiveError turns syntax errors into descriptive, action-oriented correction prompts
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)

	if strings.Contains(errStr, "Recommendation:") {
		return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s", toolName, errStr)
	}
	if strings.Contains(lowerErr, "loop detected") || strings.Contains(lowerErr, "agent halted") {
		return fmt.Sprintf("System Alert: Your execution of '%s' was blocked: %s", toolName, errStr)
	}

	var suggestion string
	if (strings.Contains(lowerErr, "oldtext block") && strings.Contains(lowerErr, "not found")) ||
		strings.Contains(lowerErr, "targetcontent not found") {
		suggestion = "The file exists, but oldText does not match its current contents. Read the file again, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write."
	} else if (strings.Contains(lowerErr, "task") && (strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no task"))) ||
		toolName == "task_status" || toolName == "task_kill" {
		suggestion = "The background task ID was not found. Check background tasks list or events to inspect valid task IDs. Do not search the filesystem for task IDs."
	} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no such file") {
		suggestion = "Inspect your immediate working directory structure using 'find' or 'list' or check the file path. Ensure the file actually exists before calling this tool."
	} else if strings.Contains(lowerErr, "escapes workspace") || strings.Contains(lowerErr, "security violation") {
		suggestion = "Verify that the path is relative or inside the current workspace. Escaping the workspace is blocked."
	} else if strings.Contains(lowerErr, "command failed") || strings.Contains(lowerErr, "exit status") {
		suggestion = "Review the command syntax and arguments. If the command depends on specific environment setups or files, verify they are present."
	} else {
		suggestion = "Ensure arguments match the schema parameters exactly, and that any files/folders referred to exist and are spelled correctly."
	}

	return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s\nRecommendation: %s", toolName, errStr, suggestion)
}

// FormatToolExecutionFailure preserves useful tool diagnostics while adding the
// action-oriented failure context expected by the agent and terminal UI.
func FormatToolExecutionFailure(toolName, output string, err error) string {
	if err == nil {
		return strings.TrimRight(output, "\r\n")
	}
	diagnostic := strings.Trim(output, "\r\n")
	if toolName == "bash" {
		if diagnostic != "" {
			if strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
				return diagnostic
			}
			if !strings.HasPrefix(diagnostic, "[Command Failed") && !strings.HasPrefix(diagnostic, "Command failed") && !strings.HasPrefix(diagnostic, "Error:") {
				errMsg := err.Error()
				if strings.HasPrefix(errMsg, "command failed: ") {
					errMsg = strings.TrimPrefix(errMsg, "command failed: ")
				}
				return fmt.Sprintf("[Command Failed: %s]\n%s", errMsg, diagnostic)
			}
			return diagnostic
		}
		return fmt.Sprintf("[Command Failed: %s]", err.Error())
	}
	alert := FormatDefensiveError(toolName, err)
	if strings.TrimSpace(diagnostic) == "" || strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
		return alert
	}
	return diagnostic + "\n\n" + alert
}

// ParseFallbackToolCalls extracts XML-style fallback tool calls from plain assistant message content
func ParseFallbackToolCalls(content string) []db.ToolCall {
	var toolCalls []db.ToolCall

	// 1. Match <tool_call name="name">args</tool_call>
	re1 := regexp.MustCompile(`<tool_call\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/tool_call>`)
	matches1 := re1.FindAllStringSubmatch(content, -1)
	for _, match := range matches1 {
		if len(match) >= 3 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			toolCalls = append(toolCalls, buildToolCall(name, args))
		}
	}

	// 2. Match <tool:name>args</tool:name>
	re2 := regexp.MustCompile(`<tool:([a-zA-Z0-9_\-]+)>(?s:(.*?))<\/tool:([a-zA-Z0-9_\-]+)>`)
	matches2 := re2.FindAllStringSubmatch(content, -1)
	for _, match := range matches2 {
		if len(match) >= 4 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			closeName := match[3]
			if name == closeName {
				toolCalls = append(toolCalls, buildToolCall(name, args))
			}
		}
	}

	// 3. Match <execute name="name">args</execute>
	re3 := regexp.MustCompile(`<execute\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/execute>`)
	matches3 := re3.FindAllStringSubmatch(content, -1)
	for _, match := range matches3 {
		if len(match) >= 3 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			toolCalls = append(toolCalls, buildToolCall(name, args))
		}
	}

	return toolCalls
}

func buildToolCall(name string, args string) db.ToolCall {
	// Try parsing as JSON first
	var temp map[string]interface{}
	isJSON := json.Unmarshal([]byte(args), &temp) == nil

	finalArgs := args
	if !isJSON {
		// Escape the args string for JSON
		escapedArgs, _ := json.Marshal(args)
		escapedArgsStr := string(escapedArgs) // this is a JSON quoted string like "my command"

		// Wrap according to tool name
		switch {
		case name == "bash" || name == "ls":
			finalArgs = fmt.Sprintf(`{"command": %s}`, escapedArgsStr)
		case name == "read":
			finalArgs = fmt.Sprintf(`{"path": %s}`, escapedArgsStr)
		case name == "grep":
			finalArgs = fmt.Sprintf(`{"pattern": %s}`, escapedArgsStr)
		case name == "load_skill":
			finalArgs = fmt.Sprintf(`{"name": %s}`, escapedArgsStr)
		case name == "task_status" || name == "task_kill":
			finalArgs = fmt.Sprintf(`{"task_id": %s}`, escapedArgsStr)
		case strings.HasPrefix(name, "subagent__"):
			finalArgs = fmt.Sprintf(`{"prompt": %s}`, escapedArgsStr)
		default:
			// Fallback: wrap as a generic string argument
			finalArgs = fmt.Sprintf(`{"arguments": %s}`, escapedArgsStr)
		}
	}

	tc := db.ToolCall{
		ID:   fmt.Sprintf("call_fallback_%s", db.NewUUID()[:8]),
		Type: "function",
	}
	tc.Function.Name = name
	tc.Function.Arguments = finalArgs
	return tc
}

// TruncateRunes safely truncates a string to maxRunes without slicing multi-byte UTF-8 runes.
func TruncateRunes(s string, maxRunes int) string {
	return style.TruncateRunes(s, maxRunes)
}

// StripEchoedPrompt strips leading echoed prompt text and trailing newlines/whitespace
// from model reasoning content if the model begins thinking by repeating the user's prompt.
func StripEchoedPrompt(reasoning, prompt string) string {
	normPrompt := strings.TrimSpace(prompt)
	if normPrompt == "" || reasoning == "" {
		return reasoning
	}

	cleanReasoning := strings.TrimLeft(reasoning, "\r\n\t ")
	lowerPrompt := strings.ToLower(normPrompt)
	lowerReasoning := strings.ToLower(cleanReasoning)

	if strings.HasPrefix(lowerReasoning, lowerPrompt) {
		remaining := cleanReasoning[len(normPrompt):]
		return strings.TrimLeft(remaining, "\r\n\t ")
	}

	// Check if the echoed prompt was wrapped in quotes, backticks, or prompt marker
	for _, wrapper := range []string{`"`, `'`, "`", "> "} {
		if strings.HasPrefix(cleanReasoning, wrapper) {
			trimmedPrefix := strings.TrimPrefix(cleanReasoning, wrapper)
			lowerTrimmed := strings.ToLower(trimmedPrefix)
			if strings.HasPrefix(lowerTrimmed, lowerPrompt) {
				remaining := trimmedPrefix[len(normPrompt):]
				if strings.HasPrefix(remaining, wrapper) {
					remaining = strings.TrimPrefix(remaining, wrapper)
				}
				return strings.TrimLeft(remaining, "\r\n\t ")
			}
		}
	}

	return reasoning
}

// IsInspectionTool returns true if the tool performs read-only inspection or searching of files.
func IsInspectionTool(name string) bool {
	switch name {
	case "read", "grep", "find", "list":
		return true
	default:
		return false
	}
}

// IsActionTool returns true if the tool executes commands, modifies files, manages tasks, or orchestrates subagents.
func IsActionTool(name string) bool {
	switch name {
	case "write", "edit", "bash", "task_kill":
		return true
	default:
		if strings.HasPrefix(name, "subagent__") || name == "spawn_subagent" || name == "remove_subagent" {
			return true
		}
		return false
	}
}

var llmControlTokenRegexes = []*regexp.Regexp{
	regexp.MustCompile(`</?atem:[^>\n<]*>?`),
	regexp.MustCompile(`</?atem:[^<\n]*`),
	regexp.MustCompile(`<\|(?:eom|im_start|im_end|start|message|endoftext|assistant|user|system)[^>|]*\|?>`),
	regexp.MustCompile(`<\|[^>\n|]+\|>`),
}

// SanitizeLLMControlTokens removes leaked provider control tokens and ChatML tags from text.
func SanitizeLLMControlTokens(s string) string {
	for _, re := range llmControlTokenRegexes {
		s = re.ReplaceAllString(s, "")
	}
	return s
}

