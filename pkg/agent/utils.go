package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
