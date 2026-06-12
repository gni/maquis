package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bidouille/pkg/agent/tool"
	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
	"bidouille/pkg/ui/style"
)

type Agent struct {
	Config         *config.Config
	ConfigPath     string
	HttpClient     *http.Client
	ActiveSkills   []Skill
	McpClients     map[string]*mcpClient
	McpClientsMu   sync.Mutex
	McpStartErrors map[string]error
	Registry       *tool.ToolRegistry
	WorkspaceRoot  string

	Tasks          map[string]*Task
	TasksMu        sync.Mutex
	NextTaskId     int
	StreamingTask  string

	ThinkingSupported      bool
	ThinkingSupportChecked bool

	PasteBuffer            string

	lastToolOutput         string
	lastToolIsError        bool
	lastToolTheme          ui.UITheme
	lastToolWasEdit        bool
	lastGenerationDuration time.Duration
}

func NewAgent(cfg *config.Config, configPath string, httpClient *http.Client) *Agent {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	absWorkspace, _ := filepath.Abs(cwd)

	a := &Agent{
		Config:         cfg,
		ConfigPath:     configPath,
		HttpClient:     httpClient,
		McpClients:     make(map[string]*mcpClient),
		McpStartErrors: make(map[string]error),
		Registry:       tool.NewToolRegistry(),
		WorkspaceRoot:  absWorkspace,
		Tasks:          make(map[string]*Task),
		NextTaskId:     1,
	}

	// Register built-in tools
	a.Registry.Register(tool.NewBashTool())
	a.Registry.Register(tool.NewReadTool())
	a.Registry.Register(tool.NewWriteTool())
	a.Registry.Register(tool.NewEditTool())
	a.Registry.Register(tool.NewGrepTool())
	a.Registry.Register(tool.NewFindTool())
	a.Registry.Register(tool.NewLsTool())
	a.Registry.Register(tool.NewLoadSkillTool())
	a.Registry.Register(tool.NewTaskStatusTool())
	a.Registry.Register(tool.NewTaskKillTool())

	return a
}

func (a *Agent) GetWorkspaceRoot() string {
	return a.WorkspaceRoot
}

func (a *Agent) GetActiveSkills() []tool.Skill {
	return a.ActiveSkills
}

func (a *Agent) SafePath(inputPath string) (string, error) {
	if inputPath == "" {
		return a.WorkspaceRoot, nil
	}

	target := inputPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(a.WorkspaceRoot, target)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	cleanRoot := filepath.Clean(a.WorkspaceRoot)
	cleanTarget := filepath.Clean(absTarget)

	if cleanTarget == cleanRoot {
		return cleanTarget, nil
	}

	// Surgical allowlist: allow writing to global memory files
	home, err := os.UserHomeDir()
	if err == nil {
		globalBidouille := filepath.Clean(filepath.Join(home, ".bidouille", "BIDOUILLE.md"))
		if cleanTarget == globalBidouille {
			return cleanTarget, nil
		}
	}

	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	if !strings.HasPrefix(cleanTarget, prefix) {
		return "", fmt.Errorf("security violation: path '%s' escapes workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	return cleanTarget, nil
}

func (a *Agent) compressHistory(
	ctx context.Context,
	messages *[]db.Message,
	sessionID string,
	theme ui.UITheme,
	w io.Writer,
) {
	if len(*messages) <= 12 {
		return
	}

	keepIdx := len(*messages) - 10
	if keepIdx < 1 {
		keepIdx = 1
	}

	toCompress := (*messages)[1:keepIdx]

	var transcriptBuilder strings.Builder
	for _, m := range toCompress {
		if m.Role == "user" {
			transcriptBuilder.WriteString(fmt.Sprintf("User: %s\n\n", m.Content))
		} else if m.Role == "assistant" {
			if m.Content != "" {
				transcriptBuilder.WriteString(fmt.Sprintf("Agent: %s\n\n", m.Content))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					transcriptBuilder.WriteString(fmt.Sprintf("Agent requested tool call: %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
		} else if m.Role == "tool" {
			out := m.Content
			if len(out) > 400 {
				out = out[:400] + "... (truncated)"
			}
			transcriptBuilder.WriteString(fmt.Sprintf("Tool Output (%s): %s\n\n", m.Name, out))
		}
	}

	transcript := transcriptBuilder.String()
	summaryPrompt := fmt.Sprintf(
		"Summarize the following developer-agent conversation transcript, preserving all key actions, decisions, file modifications, tool outputs, and technical findings in a highly concise technical summary. Format as a brief technical log:\n\n%s",
		transcript,
	)

	summaryMsgs := []db.Message{
		{
			Role:    "user",
			Content: summaryPrompt,
		},
	}

	infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, infoStyle.Render("[System: Context usage threshold reached. Compressing older conversation history...]"))

	dummyChan := make(chan StreamChunk, 100)
	go func() {
		for range dummyChan {
			// Discard summarizer stream chunks
		}
	}()

	summaryAssistantMsg, err := a.StreamChatCompletions(ctx, summaryMsgs, []string{}, dummyChan)
	if err != nil {
		warnStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s Failed to compress conversation context: %v\n", warnStyle.Render("WARNING:"), err)
		return
	}

	summaryText := summaryAssistantMsg.Content
	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf("[System: Below is a summary of the earlier conversation history:\n%s]", summaryText),
	}

	newMessages := make([]db.Message, 0, len(*messages))
	newMessages = append(newMessages, (*messages)[0]) // Keep system prompt
	newMessages = append(newMessages, summaryMsg)     // Add summary
	newMessages = append(newMessages, (*messages)[keepIdx:]...) // Add latest messages

	if sessionID != "" {
		_ = db.ClearSession(sessionID)
		for _, msg := range newMessages {
			_ = db.SaveMessage(sessionID, msg)
		}
	}
	*messages = newMessages

	successStyle := style.NewStyle().Foreground(theme.Success).Italic(true)
	fmt.Fprintf(w, "%s Context successfully compressed. Freed %d messages.\n\n", successStyle.Render("✔"), keepIdx-1)
}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	if a.Config.BeforeToolHook == "" {
		return true, ""
	}

	cmd := exec.Command("bash", "-c", a.Config.BeforeToolHook)
	cmd.Env = os.Environ()

	payload := map[string]string{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
		}
		if reason == "" {
			reason = err.Error()
		}
		return false, reason
	}
	return true, ""
}

func (a *Agent) runAfterToolHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	if a.Config.AfterToolHook == "" {
		return output, toolErr
	}

	cmd := exec.Command("bash", "-c", a.Config.AfterToolHook)
	cmd.Env = os.Environ()

	errStr := ""
	if toolErr != nil {
		errStr = toolErr.Error()
	}

	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
		"output":       output,
		"error":        errStr,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	hookOutput := stdout.String()
	if hookOutput != "" {
		return hookOutput, nil
	}
	return output, toolErr
}
