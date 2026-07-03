package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"maquis/pkg/db"
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
				sb.WriteString(fmt.Sprintf("\n\n--- Global Memory & Personalization Context (%s) ---\n%s\n--- End Global Memory ---", globalPath, trimmed))
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
					sb.WriteString(fmt.Sprintf("\n\n--- Project Memory & Learnings (%s) ---\n%s\n--- End Project Memory ---", projectPath, trimmed))
				}
				break
			}

			projectDotPath := filepath.Join(dir, ".maquis", "MEMORY.md")
			if data, err := os.ReadFile(projectDotPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\n--- Project Memory & Learnings (%s) ---\n%s\n--- End Project Memory ---", projectDotPath, trimmed))
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

func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	// Find the last assistant message
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	globalPrompt := 0
	globalCompletion := 0

	// Sum all completion tokens
	for _, m := range messages {
		if m.Role == "assistant" {
			if m.CompletionTokens > 0 {
				globalCompletion += m.CompletionTokens
			} else {
				globalCompletion += (len(m.Content) + len(m.ReasoningContent)) / 4
			}
		}
	}

	var toolsEst int
	if a.Registry != nil {
		tools := a.Registry.GetAvailableTools(allowedTools)
		var toolsChars int
		if len(tools) > 0 {
			if toolsData, err := json.Marshal(tools); err == nil {
				toolsChars = len(toolsData)
			}
		}
		toolsEst = toolsChars / 4
	}

	if lastAssistantIdx != -1 {
		// We have an assistant message.
		// Start with the prompt tokens of that turn.
		if messages[lastAssistantIdx].PromptTokens > 0 {
			globalPrompt = messages[lastAssistantIdx].PromptTokens
		} else {
			// Fallback if not stored
			for i := 0; i <= lastAssistantIdx; i++ {
				m := messages[i]
				if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
					globalPrompt += len(m.Content) / 4
				}
			}
			globalPrompt += toolsEst
		}

		// Add estimation for any messages after the last assistant message
		for i := lastAssistantIdx + 1; i < len(messages); i++ {
			m := messages[i]
			if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
				globalPrompt += len(m.Content) / 4
			}
		}
	} else {
		// No assistant messages yet. Estimate everything.
		for _, m := range messages {
			if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
				globalPrompt += len(m.Content) / 4
			}
		}
		if globalPrompt == 0 {
			globalPrompt = len(a.GetSystemPrompt()) / 4
		}
		globalPrompt += toolsEst
	}

	return globalPrompt, globalCompletion
}


// FormatDefensiveError turns syntax errors into descriptive, action-oriented correction prompts
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()

	var suggestion string
	if strings.Contains(errStr, "not found") || strings.Contains(errStr, "no such file") {
		suggestion = "Inspect your immediate working directory structure using 'bash' with 'ls' or check the file path. Ensure the file actually exists before calling this tool."
	} else if strings.Contains(errStr, "oldText block not found") || strings.Contains(errStr, "targetContent not found") {
		suggestion = "The exact text block you tried to replace was not found. Read the file content first to verify the exact indentation, line endings, and characters, then try again with a precise match."
	} else if strings.Contains(errStr, "escapes workspace") || strings.Contains(errStr, "security violation") {
		suggestion = "Verify that the path is relative or inside the current workspace. Escaping the workspace is blocked."
	} else if strings.Contains(errStr, "command failed") || strings.Contains(errStr, "exit status") {
		suggestion = "Review the command syntax and arguments. If the command depends on specific environment setups or files, verify they are present."
	} else {
		suggestion = "Ensure arguments match the schema parameters exactly, and that any files/folders referred to exist and are spelled correctly."
	}

	return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s\nRecommendation: %s", toolName, errStr, suggestion)
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
		case name == "ls":
			finalArgs = fmt.Sprintf(`{"command": %s}`, escapedArgsStr)
		case name == "read":
			finalArgs = fmt.Sprintf(`{"path": %s}`, escapedArgsStr)
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


