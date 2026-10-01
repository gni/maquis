package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/db"
)

// DebugLogger records complete execution traces, function calls, arguments,
// LLM payloads, and repetition statistics to a persistent file.
type DebugLogger struct {
	mu             sync.Mutex
	filePath       string
	file           *os.File
	fileReadCounts map[string]int
}

// NewDebugLogger creates a new DebugLogger targeting customPath, MAQUIS_DEBUG_FILE,
// or defaulting to maquis_debug.log in workspaceRoot.
// If customPath or MAQUIS_DEBUG_FILE is "off", "none", "false", "disabled", or "/dev/null",
// debug logging is disabled and nil is returned.
func NewDebugLogger(workspaceRoot string, customPath string) *DebugLogger {
	target := strings.TrimSpace(customPath)
	if target == "" {
		target = strings.TrimSpace(os.Getenv("MAQUIS_DEBUG_FILE"))
	}
	if strings.EqualFold(target, "off") || strings.EqualFold(target, "none") || strings.EqualFold(target, "false") || strings.EqualFold(target, "disabled") || target == "/dev/null" || target == "null" {
		return nil
	}
	if target == "" {
		if workspaceRoot == "" {
			workspaceRoot = "."
		}
		target = filepath.Join(workspaceRoot, "maquis_debug.log")
	}

	absTarget, err := filepath.Abs(target)
	if err == nil {
		target = absTarget
	}

	dir := filepath.Dir(target)
	_ = os.MkdirAll(dir, 0755)

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return &DebugLogger{
			filePath:       target,
			fileReadCounts: make(map[string]int),
		}
	}

	dl := &DebugLogger{
		filePath:       target,
		file:           f,
		fileReadCounts: make(map[string]int),
	}

	dl.writeRaw(fmt.Sprintf("\n%s\n[SESSION START: %s]\nDebug Log File: %s\n%s\n",
		strings.Repeat("=", 80),
		time.Now().Format("2006-01-02 15:04:05.000"),
		target,
		strings.Repeat("=", 80),
	))

	return dl
}

// FilePath returns the absolute path to the debug log file.
func (dl *DebugLogger) FilePath() string {
	if dl == nil {
		return ""
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.filePath
}

func (dl *DebugLogger) writeRaw(content string) {
	if dl == nil || dl.file == nil {
		return
	}
	_, _ = dl.file.WriteString(content)
	_ = dl.file.Sync()
}

// ResetTurnCounts resets per-turn counters (such as file reread trackers).
func (dl *DebugLogger) ResetTurnCounts() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.fileReadCounts = make(map[string]int)
}

// LogUserCommand records a newly submitted prompt or command.
func (dl *DebugLogger) LogUserCommand(sessionID string, prompt string) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	dl.fileReadCounts = make(map[string]int)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s\n", strings.Repeat("=", 80)))
	sb.WriteString(fmt.Sprintf("[%s] USER COMMAND (Session: %s)\n", time.Now().Format("2006-01-02 15:04:05.000"), sessionID))
	sb.WriteString(fmt.Sprintf("Prompt: %s\n", strings.TrimSpace(prompt)))
	sb.WriteString(fmt.Sprintf("%s\n", strings.Repeat("=", 80)))

	dl.writeRaw(sb.String())
}

// LogLLMRequest records the payload and tool loadout sent to the LLM.
func (dl *DebugLogger) LogLLMRequest(sessionID string, iter int, model, endpoint string, messages []db.Message, tools []tool.Tool) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s\n", strings.Repeat("-", 80)))
	sb.WriteString(fmt.Sprintf("[%s] LLM REQUEST (Turn Iteration %d) (Session: %s)\n", time.Now().Format("2006-01-02 15:04:05.000"), iter, sessionID))
	sb.WriteString(fmt.Sprintf("Model: %s | Endpoint: %s\n", model, endpoint))
	sb.WriteString(fmt.Sprintf("Messages Count: %d\n", len(messages)))

	// Print message summary
	for i, m := range messages {
		contentPreview := strings.TrimSpace(m.Content)
		if len(contentPreview) > 300 {
			contentPreview = contentPreview[:300] + "... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("  [%d] Role: %-9s", i+1, m.Role))
		if m.ToolCallID != "" {
			sb.WriteString(fmt.Sprintf(" | ToolCallID: %s | Name: %s", m.ToolCallID, m.Name))
		}
		if len(m.ToolCalls) > 0 {
			var tcNames []string
			for _, tc := range m.ToolCalls {
				tcNames = append(tcNames, fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments))
			}
			sb.WriteString(fmt.Sprintf(" | Emitted ToolCalls: %s", strings.Join(tcNames, ", ")))
		}
		sb.WriteString(fmt.Sprintf("\n      Content: %q\n", contentPreview))
	}

	// Print tools list
	if len(tools) > 0 {
		var toolNames []string
		for _, t := range tools {
			toolNames = append(toolNames, t.Function.Name)
		}
		sb.WriteString(fmt.Sprintf("Tools Offered (%d): %s\n", len(tools), strings.Join(toolNames, ", ")))
	}
	sb.WriteString(fmt.Sprintf("%s\n", strings.Repeat("-", 80)))

	dl.writeRaw(sb.String())
}

// LogLLMResponse records the LLM completion response, reasoning thoughts, and proposed tool calls.
func (dl *DebugLogger) LogLLMResponse(sessionID string, iter int, msg *db.Message, duration time.Duration) {
	if dl == nil || msg == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s\n", strings.Repeat("-", 80)))
	sb.WriteString(fmt.Sprintf("[%s] LLM RESPONSE (Turn Iteration %d) (Duration: %v) (Session: %s)\n",
		time.Now().Format("2006-01-02 15:04:05.000"), iter, duration.Round(time.Millisecond), sessionID))

	if msg.ReasoningContent != "" {
		sb.WriteString("Reasoning / Thoughts:\n")
		sb.WriteString("  " + strings.ReplaceAll(strings.TrimSpace(msg.ReasoningContent), "\n", "\n  ") + "\n")
	}

	if strings.TrimSpace(msg.Content) != "" {
		sb.WriteString(fmt.Sprintf("Assistant Content: %s\n", strings.TrimSpace(msg.Content)))
	}

	if len(msg.ToolCalls) > 0 {
		sb.WriteString(fmt.Sprintf("Tool Calls Proposed (%d):\n", len(msg.ToolCalls)))
		for i, tc := range msg.ToolCalls {
			formattedArgs := formatJSONOrRaw(tc.Function.Arguments)
			sb.WriteString(fmt.Sprintf("  #%d [%s] Function: %s\n", i+1, tc.ID, tc.Function.Name))
			sb.WriteString(fmt.Sprintf("      Arguments: %s\n", strings.ReplaceAll(formattedArgs, "\n", "\n      ")))
		}
	} else {
		sb.WriteString("No Tool Calls Proposed (Final Turn Output)\n")
	}
	sb.WriteString(fmt.Sprintf("%s\n", strings.Repeat("-", 80)))

	dl.writeRaw(sb.String())
}

// LogToolExecution records a function execution with exact arguments, return result,
// duration, and detects repetitive file inspection patterns.
func (dl *DebugLogger) LogToolExecution(sessionID string, iter int, toolName string, arguments string, output string, err error, duration time.Duration) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	status := "SUCCESS"
	if err != nil {
		status = fmt.Sprintf("ERROR (%v)", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s\n", strings.Repeat("-", 80)))
	sb.WriteString(fmt.Sprintf("[%s] TOOL EXECUTION: %s | Status: %s | Duration: %v\n",
		time.Now().Format("2006-01-02 15:04:05.000"), toolName, status, duration.Round(time.Millisecond)))

	formattedArgs := formatJSONOrRaw(arguments)
	sb.WriteString(fmt.Sprintf("Arguments:\n  %s\n", strings.ReplaceAll(formattedArgs, "\n", "\n  ")))

	// Special repetition diagnostics for file operations
	if toolName == "read" {
		var readArgs struct {
			Path     string `json:"path"`
			File     string `json:"file"`
			FilePath string `json:"file_path"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(arguments), &readArgs)
		targetFile := readArgs.Path
		if targetFile == "" {
			targetFile = readArgs.FilePath
		}
		if targetFile == "" {
			targetFile = readArgs.File
		}
		if targetFile != "" {
			dl.fileReadCounts[targetFile]++
			count := dl.fileReadCounts[targetFile]
			if count > 1 {
				sb.WriteString(fmt.Sprintf("⚠️  [RE-READ DETECTED #%d] Target file '%s' has been read %d times this turn! (offset=%d, limit=%d)\n",
					count, targetFile, count, readArgs.Offset, readArgs.Limit))
			}
		}
	}

	lines := strings.Split(output, "\n")
	sb.WriteString(fmt.Sprintf("Output (%d bytes, %d lines):\n", len(output), len(lines)))

	// Cap output preview in debug log if extremely long, but keep enough for diagnosis
	const maxOutputDebugLen = 4096
	if len(output) <= maxOutputDebugLen {
		sb.WriteString("  " + strings.ReplaceAll(strings.TrimRight(output, "\r\n"), "\n", "\n  ") + "\n")
	} else {
		sb.WriteString("  " + strings.ReplaceAll(output[:2500], "\n", "\n  ") + "\n")
		sb.WriteString(fmt.Sprintf("  ... [%d bytes omitted from debug display] ...\n", len(output)-3500))
		sb.WriteString("  " + strings.ReplaceAll(output[len(output)-1000:], "\n", "\n  ") + "\n")
	}

	sb.WriteString(fmt.Sprintf("%s\n", strings.Repeat("-", 80)))

	dl.writeRaw(sb.String())
}

// LogRepetition records an alert when the loop breaker detects repetitive calls.
func (dl *DebugLogger) LogRepetition(sessionID string, toolName string, arguments string, count int, detail string) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s\n", strings.Repeat("!", 80)))
	sb.WriteString(fmt.Sprintf("[%s] REPETITION WARNING | Function: %s | Repeated: %d times\n",
		time.Now().Format("2006-01-02 15:04:05.000"), toolName, count))
	sb.WriteString(fmt.Sprintf("Arguments: %s\n", strings.TrimSpace(arguments)))
	if detail != "" {
		sb.WriteString(fmt.Sprintf("Detail: %s\n", detail))
	}
	sb.WriteString(fmt.Sprintf("%s\n", strings.Repeat("!", 80)))

	dl.writeRaw(sb.String())
}

// Close flushes and closes the underlying debug log file.
func (dl *DebugLogger) Close() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.file != nil {
		_ = dl.file.Sync()
		_ = dl.file.Close()
		dl.file = nil
	}
}

func formatJSONOrRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(empty)"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err == nil {
		return buf.String()
	}
	return raw
}
