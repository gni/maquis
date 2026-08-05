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
	"sort"
	"strings"
	"sync"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

type StreamRenderer interface {
	Write(content string)
	WriteReasoning(content string)
	Flush()
	HasOutput() bool
	StartToolCall(toolName string, toolCallIndex int)
	WriteToolCall(content string)
	GetToolTitleLineNumber(index int) int
	DidStreamToolBody(index int) bool
	CompleteToolCall(index int, toolName string, toolArgs string, isError bool)
	GetReasoningDuration() float64
}

type SubagentCancellationDecision uint8

const (
	SubagentCancellationContinue SubagentCancellationDecision = iota
	SubagentCancellationSkipCurrent
	SubagentCancellationStopAll
)

type AgentUI interface {
	DrawStatusBar(w io.Writer, theme style.UITheme)
	DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string)
	NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) StreamRenderer
	SetCollapseStatus(collapsed bool)
	UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool)
	DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string)
	AskForApproval(w io.Writer, theme style.UITheme) (bool, bool)
	AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) SubagentCancellationDecision
	RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string)
	RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool)
	SetCursorHidden(hidden bool)
}

type Agent struct {
	UI             AgentUI
	LLMProvider    LLMProvider
	LLMProviderMu  sync.RWMutex
	Config         *config.Config
	ConfigPath     string
	HttpClient     *http.Client
	ActiveSkills   []Skill
	McpClients     map[string]*mcpClient
	McpClientsMu   sync.Mutex
	McpStartErrors map[string]error
	Registry       *tool.ToolRegistry
	WorkspaceRoot  string

	Tasks         map[string]*Task
	TasksMu       sync.Mutex
	NextTaskId    int
	StreamingTask string

	CurrentStreamBuffer *bytes.Buffer
	CurrentStreamMu     sync.Mutex

	ThinkingSupported      bool
	ThinkingSupportChecked bool

	CurrentWriter  io.Writer
	CurrentContext context.Context

	lastToolOutput         string
	lastToolIsError        bool
	lastToolWasEdit        bool
	lastGenerationDuration time.Duration

	TurnStartTime time.Time

	SpawnedAgents   map[string]bool
	SpawnedAgentsMu sync.RWMutex

	SystemEvents       chan string
	PendingSystemEvent string

	ClearAgentsFunc   func()
	MultiAgentManager *MultiAgentManager
}

func NewAgent(cfg *config.Config, configPath string, httpClient *http.Client) *Agent {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	absWorkspace, _ := filepath.Abs(cwd)

	a := &Agent{
		Config:     cfg,
		ConfigPath: configPath,
		HttpClient: httpClient,
		LLMProvider: &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: httpClient,
		},
		McpClients:     make(map[string]*mcpClient),
		McpStartErrors: make(map[string]error),
		Registry:       tool.NewToolRegistry(),
		WorkspaceRoot:  absWorkspace,
		Tasks:          make(map[string]*Task),
		NextTaskId:     1,
		SpawnedAgents:  make(map[string]bool),
		SystemEvents:   make(chan string, 100),
	}

	// Register built-in tools
	a.Registry.Register(tool.NewBashTool())
	a.Registry.Register(tool.NewReadTool())
	a.Registry.Register(tool.NewWriteTool())
	a.Registry.Register(tool.NewEditTool())
	a.Registry.Register(tool.NewLoadSkillTool())
	a.Registry.Register(tool.NewTaskStatusTool())
	a.Registry.Register(tool.NewTaskKillTool())

	// Only register local executable plugins from the "plugins" directory in the workspace
	if !a.Config.DisableLocalPlugins {
		pluginsDir := filepath.Join(absWorkspace, "plugins")
		_ = tool.RegisterPlugins(a.Registry, pluginsDir)
	}

	return a
}

// ApplyConfig replaces the live configuration and refreshes the built-in LLM
// provider so the next request uses the new endpoint, credentials, and model.
func (a *Agent) ApplyConfig(cfg *config.Config) {
	if a == nil || cfg == nil {
		return
	}

	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()

	a.Config = cfg
	switch provider := a.LLMProvider.(type) {
	case nil:
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: a.HttpClient,
		}
	case *OpenAICompatibleProvider:
		httpClient := provider.HttpClient
		if httpClient == nil {
			httpClient = a.HttpClient
		}
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: httpClient,
		}
	}
	a.ThinkingSupported = false
	a.ThinkingSupportChecked = false
}

func (a *Agent) GetWorkspaceRoot() string {
	return a.WorkspaceRoot
}

func (a *Agent) Context() context.Context {
	if a.CurrentContext != nil {
		return a.CurrentContext
	}
	return context.Background()
}

func (a *Agent) GetActiveSkills() []tool.Skill {
	return a.ActiveSkills
}

func (a *Agent) ReloadSkills() []tool.Skill {
	if a == nil || a.Config == nil {
		if a != nil {
			return a.ActiveSkills
		}
		return nil
	}
	cwd := a.WorkspaceRoot
	var dirs []string
	if a.Config.SkillsDir != "" {
		dirs = append(dirs, a.Config.SkillsDir)
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, "skills"), filepath.Join(cwd, ".agents", "skills"))
	}
	if len(dirs) == 0 {
		return a.ActiveSkills
	}
	skills, err := LoadSkillsFromDirs(dirs...)
	if err == nil && (len(skills) > 0 || len(a.ActiveSkills) == 0) {
		a.ActiveSkills = skills
	}
	return a.ActiveSkills
}

func (a *Agent) ReloadPlugins() error {
	if a.Config.DisableLocalPlugins {
		return fmt.Errorf("local plugins are disabled for this workspace (run with trust to enable)")
	}

	// Clear all existing plugins (tools prefixed with "plugin__")
	a.Registry.UnregisterPrefix("plugin__")

	// Re-register local plugins from the workspace
	pluginsDir := filepath.Join(a.WorkspaceRoot, "plugins")
	return tool.RegisterPlugins(a.Registry, pluginsDir)
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

	// Surgical security checks: block modifying active configuration file or session databases
	if a.ConfigPath != "" {
		absConfig, errConfig := filepath.Abs(a.ConfigPath)
		if errConfig == nil {
			cleanConfig := filepath.Clean(absConfig)
			if cleanTarget == cleanConfig {
				return "", fmt.Errorf("security violation: modifying the active configuration file is not allowed")
			}
			cleanSessionsDir := filepath.Clean(filepath.Join(filepath.Dir(absConfig), "sessions"))
			if cleanTarget == cleanSessionsDir || strings.HasPrefix(cleanTarget, cleanSessionsDir+string(filepath.Separator)) {
				return "", fmt.Errorf("security violation: modifying session database files is not allowed")
			}
		}
	}

	// Surgical allowlist: allow writing to global memory files
	home, err := os.UserHomeDir()
	if err == nil {
		globalMaquis := filepath.Clean(filepath.Join(home, ".maquis", "MAQUIS.md"))
		if cleanTarget == globalMaquis {
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
	theme style.UITheme,
	w io.Writer,
) {
	keepMsgCount := 4
	if len(*messages) <= keepMsgCount+2 {
		keepMsgCount = 2
	}

	keepIdx := len(*messages) - keepMsgCount
	if keepIdx <= 1 {
		return
	}

	toCompress := (*messages)[1:keepIdx]

	var transcriptBuilder strings.Builder
	readFilesMap := make(map[string]bool)
	modifiedFilesMap := make(map[string]bool)

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
					var fileArg struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &fileArg) == nil && fileArg.Path != "" {
						if tc.Function.Name == "read" {
							readFilesMap[fileArg.Path] = true
						} else if tc.Function.Name == "write" || tc.Function.Name == "edit" {
							modifiedFilesMap[fileArg.Path] = true
						}
					}
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

	var readFiles, modifiedFiles []string
	for f := range readFilesMap {
		readFiles = append(readFiles, f)
	}
	for f := range modifiedFilesMap {
		modifiedFiles = append(modifiedFiles, f)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)

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
	var fileOpsHeader string
	if len(readFiles) > 0 || len(modifiedFiles) > 0 {
		fileOpsHeader = fmt.Sprintf("\n- Files Read: %s\n- Files Modified: %s\n", strings.Join(readFiles, ", "), strings.Join(modifiedFiles, ", "))
	}
	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf("[System: Below is a summary of the earlier conversation history:%s\n%s]", fileOpsHeader, summaryText),
	}

	keptMessages := make([]db.Message, 0, len((*messages)[keepIdx:]))
	for _, m := range (*messages)[keepIdx:] {
		if m.Role == "tool" && len(m.Content) > 1000 {
			m.Content = m.Content[:1000] + "... (truncated for context optimization)"
		}
		keptMessages = append(keptMessages, m)
	}

	newMessages := make([]db.Message, 0, 2+len(keptMessages))
	newMessages = append(newMessages, (*messages)[0])           // Keep system prompt
	newMessages = append(newMessages, summaryMsg)               // Add summary
	newMessages = append(newMessages, keptMessages...)          // Add latest messages

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

func (a *Agent) HasSubagent(name string) bool {
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return a.SpawnedAgents[name]
}
