package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

// MultiAgent represents an independent agent node in a multi-agent swarm.
type MultiAgent struct {
	Name         string
	SystemPrompt string
	History      []db.Message
	HistoryMu    sync.RWMutex
	Skills       []tool.Skill
	LocalSkills  []tool.Skill
	HasAllSkills bool
	Input        chan db.Message
	Output       chan db.Message
	Subagents    map[string]*MultiAgent
	SubagentsMu  sync.RWMutex
	BaseAgent    *Agent
	Parent       *MultiAgent
	Manager      *MultiAgentManager
	Context      context.Context
	Cancel       context.CancelFunc

	ActiveContext context.Context
	ActiveCancel  context.CancelFunc
	ActiveStarted time.Time
}

// GetSystemPrompt generates the system instructions and reference guides list for the agent.
func (ma *MultiAgent) GetSystemPrompt() string {
	var activeAgents []string
	if ma.BaseAgent != nil {
		ma.BaseAgent.SpawnedAgentsMu.RLock()
		for name := range ma.BaseAgent.SpawnedAgents {
			activeAgents = append(activeAgents, name)
		}
		ma.BaseAgent.SpawnedAgentsMu.RUnlock()
		sort.Strings(activeAgents)
	}

	workspaceRoot := "."
	skillsDir := "skills"
	compact := false
	var allSkills []tool.Skill
	if ma.BaseAgent != nil {
		workspaceRoot = ma.BaseAgent.WorkspaceRoot
		allSkills = ma.BaseAgent.ActiveSkills
		if ma.BaseAgent.Config != nil {
			skillsDir = ma.BaseAgent.Config.SkillsDir
			compact = ma.BaseAgent.Config.CompactPrompt
		}
	}

	return BuildSystemPrompt(SystemPromptConfig{
		BaseInstruction: ma.SystemPrompt,
		WorkspaceRoot:   workspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		ActiveAgents:    activeAgents,
		Skills:          ma.Skills,
		AllSkills:       allSkills,
	})
}

// NewMultiAgent creates a new MultiAgent node.
func NewMultiAgent(name string, systemPrompt string, parent *MultiAgent, baseAgent *Agent, skillNames []string) *MultiAgent {
	var skills []tool.Skill
	if len(skillNames) > 0 {
		for _, sn := range skillNames {
			for _, s := range baseAgent.ActiveSkills {
				if s.Name == sn {
					skills = append(skills, s)
					break
				}
			}
		}
	} else {
		skills = make([]tool.Skill, len(baseAgent.ActiveSkills))
		copy(skills, baseAgent.ActiveSkills)
	}

	return newMultiAgentWithSkills(name, systemPrompt, parent, baseAgent, skills, nil, len(skillNames) == 0)
}

func newMultiAgentWithSkills(
	name string,
	systemPrompt string,
	parent *MultiAgent,
	baseAgent *Agent,
	referenceSkills []tool.Skill,
	localSkills []tool.Skill,
	hasAllSkills bool,
) *MultiAgent {
	ctx, cancel := context.WithCancel(context.Background())

	skills := make([]tool.Skill, 0, len(referenceSkills)+len(localSkills))
	skills = append(skills, referenceSkills...)
	skills = append(skills, localSkills...)

	ma := &MultiAgent{
		Name:         name,
		SystemPrompt: systemPrompt,
		Parent:       parent,
		Skills:       skills,
		LocalSkills:  append([]tool.Skill(nil), localSkills...),
		HasAllSkills: hasAllSkills,
		Input:        make(chan db.Message, 100),
		Output:       make(chan db.Message, 100),
		Subagents:    make(map[string]*MultiAgent),
		BaseAgent:    baseAgent,
		Context:      ctx,
		Cancel:       cancel,
	}

	ma.History = []db.Message{
		{Role: "system", Content: ma.GetSystemPrompt()},
	}

	return ma
}

func (ma *MultiAgent) CancelActiveTurn() {
	ma.HistoryMu.RLock()
	cancel := ma.ActiveCancel
	ma.HistoryMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (mam *MultiAgentManager) ActiveSubagentName() (string, bool) {
	type candidate struct {
		name    string
		started time.Time
		depth   int
	}

	mam.mu.RLock()
	agents := make([]*MultiAgent, 0, len(mam.Agents))
	for _, ma := range mam.Agents {
		agents = append(agents, ma)
	}
	mam.mu.RUnlock()

	var current candidate
	found := false
	for _, ma := range agents {
		ma.HistoryMu.RLock()
		activeContext := ma.ActiveContext
		activeCancel := ma.ActiveCancel
		started := ma.ActiveStarted
		ma.HistoryMu.RUnlock()
		if activeContext == nil || activeCancel == nil || activeContext.Err() != nil {
			continue
		}

		depth := 0
		for parent := ma.Parent; parent != nil; parent = parent.Parent {
			depth++
		}
		next := candidate{name: ma.Name, started: started, depth: depth}
		if !found ||
			next.started.After(current.started) ||
			(next.started.Equal(current.started) && next.depth > current.depth) ||
			(next.started.Equal(current.started) && next.depth == current.depth && next.name < current.name) {
			current = next
			found = true
		}
	}

	return current.name, found
}

func (mam *MultiAgentManager) CancelSubagentTurn(name string) bool {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return false
	}

	ma.HistoryMu.RLock()
	activeContext := ma.ActiveContext
	cancel := ma.ActiveCancel
	ma.HistoryMu.RUnlock()
	if activeContext == nil || cancel == nil || activeContext.Err() != nil {
		return false
	}
	cancel()
	return true
}

func (mam *MultiAgentManager) CancelAllActiveSubagents() []string {
	mam.mu.RLock()
	names := make([]string, 0, len(mam.Agents))
	for name := range mam.Agents {
		names = append(names, name)
	}
	mam.mu.RUnlock()
	sort.Strings(names)

	cancelled := make([]string, 0, len(names))
	for _, name := range names {
		if mam.CancelSubagentTurn(name) {
			cancelled = append(cancelled, name)
		}
	}
	return cancelled
}

func (ma *MultiAgent) GetToolAllowlist() []string {
	var allowlist []string

	executors := ma.BaseAgent.Registry.GetAllExecutors()

	ma.SubagentsMu.RLock()
	children := make(map[string]bool)
	for name := range ma.Subagents {
		children[name] = true
	}
	ma.SubagentsMu.RUnlock()

	for name := range executors {
		if strings.HasPrefix(name, "subagent__") {
			subagentName := strings.TrimPrefix(name, "subagent__")
			if children[subagentName] {
				allowlist = append(allowlist, name)
			}
		} else {
			allowlist = append(allowlist, name)
		}
	}

	return allowlist
}

func (ma *MultiAgent) Start(w io.Writer, theme style.UITheme) {
	go func() {
		for {
			select {
			case <-ma.Context.Done():
				return
			case msg, ok := <-ma.Input:
				if !ok {
					return
				}

				ma.HistoryMu.Lock()
				ma.History = append(ma.History, msg)
				ma.HistoryMu.Unlock()

				taskID := msg.ToolCallID
				if taskID != "" && ma.Manager != nil {
					ma.Manager.UpdateTaskStatus(taskID, "running", "", nil)
					_ = ma.Manager.SaveAgentState(ma, "running")
				} else if ma.Manager != nil {
					_ = ma.Manager.SaveAgentState(ma, "running")
				}

				if msg.Role == "user" {
					writer := w
					if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter != nil {
						writer = ma.BaseAgent.CurrentWriter
					}
					fmt.Fprintf(writer, "\n[%s] received task from %s: %s\n",
						style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
						msg.Name,
						msg.Content,
					)

					turnCtx, turnCancel := context.WithCancel(ma.Context)
					ma.HistoryMu.Lock()
					ma.ActiveContext = turnCtx
					ma.ActiveCancel = turnCancel
					ma.ActiveStarted = time.Now()
					ma.HistoryMu.Unlock()

					response, err := ma.executeLoop(turnCtx, writer, theme)

					turnCancel()
					ma.HistoryMu.Lock()
					ma.ActiveContext = nil
					ma.ActiveCancel = nil
					ma.ActiveStarted = time.Time{}
					ma.HistoryMu.Unlock()

					if err != nil {
						if taskID != "" && ma.Manager != nil {
							ma.Manager.UpdateTaskStatus(taskID, "failed", "", err)
						}
						if ma.Manager != nil {
							_ = ma.Manager.SaveAgentState(ma, "failed")
						}

						if err != context.Canceled {
							errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
							fmt.Fprintf(writer, "\n%s [%s] error: %v\n",
								errStyle.Render("!"),
								style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
								err,
							)
						}
						errMsg := db.Message{
							Role:    "assistant",
							Name:    ma.Name,
							Content: fmt.Sprintf("Error: %v", err),
						}
						select {
						case ma.Output <- errMsg:
						default:
						}
						continue
					}

					if taskID != "" && ma.Manager != nil {
						ma.Manager.UpdateTaskStatus(taskID, "completed", response.Content, nil)
					}
					if ma.Manager != nil {
						_ = ma.Manager.SaveAgentState(ma, "idle")
					}

					select {
					case ma.Output <- response:
					default:
					}
				}
			}
		}
	}()
}

func (ma *MultiAgent) executeLoop(ctx context.Context, w io.Writer, theme style.UITheme) (db.Message, error) {
	writer := w
	if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter != nil {
		writer = ma.BaseAgent.CurrentWriter
	}
	rawW := unwrapWriter(writer)

	maxSteps := ma.BaseAgent.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}

	callCounts := make(map[string]int)
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return db.Message{}, ctx.Err()
		}
		ma.HistoryMu.RLock()
		historyCopy := make([]db.Message, len(ma.History))
		copy(historyCopy, ma.History)
		ma.HistoryMu.RUnlock()

		chunkChan := make(chan StreamChunk, 100)
		errChan := make(chan error, 1)

		var assistantMsg *db.Message
		go func() {
			allowlist := ma.GetToolAllowlist()
			msg, err := ma.BaseAgent.StreamChatCompletions(ctx, historyCopy, allowlist, chunkChan)
			errChan <- err
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
		}()

		if ma.BaseAgent != nil {
			ma.BaseAgent.CurrentStreamMu.Lock()
			ma.BaseAgent.CurrentStreamBuffer = new(bytes.Buffer)
			ma.BaseAgent.CurrentStreamMu.Unlock()
			teeWriter := &customTeeWriter{screen: writer, buffer: ma.BaseAgent.CurrentStreamBuffer}
			writer = teeWriter
		}
		var lastUserPrompt string
		for i := len(historyCopy) - 1; i >= 0; i-- {
			if historyCopy[i].Role == "user" {
				lastUserPrompt = historyCopy[i].Content
				break
			}
		}

		ncw := &newlineCounterWriter{Writer: writer}
		var sr StreamRenderer
		if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
			sr = ma.BaseAgent.UI.NewStreamRenderer(ncw, theme, ma.BaseAgent.Config.ShowThinking, ma.BaseAgent.Config.StreamWrites, ma.Name)
		} else {
			sr = &fallbackStreamRenderer{w: ncw}
		}
		sr.SetPrompt(lastUserPrompt)

		var responseHeaderStarted bool
		var subagentCompletionTokens int
		var subagentGenStart time.Time
		var lastDraw time.Time

		for chunk := range chunkChan {
			if subagentGenStart.IsZero() {
				subagentGenStart = time.Now()
				lastDraw = subagentGenStart
			}

			if chunk.Type == "reasoning" || chunk.Type == "text" {
				subagentCompletionTokens++
			}

			if chunk.Type == "reasoning" {
				if ma.BaseAgent.Config.ShowThinking {
					if !responseHeaderStarted {
						fmt.Fprintf(ncw, "\n[%s] response: ",
							style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
						)
						responseHeaderStarted = true
					}
					sr.WriteReasoning(chunk.Content)
				}
			} else {
				if !responseHeaderStarted {
					fmt.Fprintf(ncw, "\n[%s] response: ",
						style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
					)
					responseHeaderStarted = true
				}

				if chunk.Type == "text" {
					sr.Write(chunk.Content)
				} else if chunk.Type == "tool_name" {
					if chunk.ToolCallIndex == 0 {
						sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
					}
				} else if chunk.Type == "tool_call" {
					if chunk.ToolCallIndex == 0 {
						sr.WriteToolCall(chunk.Content)
					}
				}
			}

			now := time.Now()
			if ma.Parent == nil && now.Sub(lastDraw) >= 100*time.Millisecond && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
				elapsed := now.Sub(subagentGenStart).Seconds()
				var tps float64
				if elapsed > 0 {
					tps = float64(subagentCompletionTokens) / elapsed
				}

				ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, subagentCompletionTokens, ma.BaseAgent.Config.ContextWindowLimit, true, tps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
				ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
				lastDraw = now
			}
		}
		sr.Flush()

		if ma.Parent == nil && ma.BaseAgent != nil && !subagentGenStart.IsZero() {
			elapsed := time.Since(subagentGenStart).Seconds()
			var finalTps float64
			if elapsed > 0 {
				finalTps = float64(subagentCompletionTokens) / elapsed
			}

			ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, subagentCompletionTokens, ma.BaseAgent.Config.ContextWindowLimit, false, finalTps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
			ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
		}

		if ma.BaseAgent != nil {
			ma.BaseAgent.CurrentStreamMu.Lock()
			ma.BaseAgent.CurrentStreamBuffer = nil
			ma.BaseAgent.CurrentStreamMu.Unlock()
		}

		err := <-errChan
		if err != nil {
			return db.Message{}, err
		}

		if assistantMsg == nil {
			return db.Message{}, fmt.Errorf("received empty completion response")
		}

		ma.HistoryMu.Lock()
		ma.History = append(ma.History, *assistantMsg)
		ma.HistoryMu.Unlock()
		if ma.Manager != nil {
			_ = ma.Manager.SaveAgentState(ma, "running")
		}

		if assistantMsg != nil && lastUserPrompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, lastUserPrompt)
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if !responseHeaderStarted {
				fmt.Fprintf(ncw, "\n[%s] response: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
					assistantMsg.Content,
				)
			} else {
				fmt.Fprintln(ncw)
			}
			return *assistantMsg, nil
		}

		for idx, tc := range assistantMsg.ToolCalls {
			if ctx.Err() != nil {
				return db.Message{}, ctx.Err()
			}

			isSubagent := strings.HasPrefix(tc.Function.Name, "subagent__")
			wasStreamed := sr.GetToolTitleLineNumber(idx) != -1

			if !wasStreamed || (len(assistantMsg.ToolCalls) > 1 && idx > 0) {
				prefixStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
				fmt.Fprintf(ncw, "%s [%s] calling tool:\n",
					style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("❖"),
					prefixStyle.Render(ma.Name),
				)
				if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
					ma.BaseAgent.UI.RenderToolHeader(ncw, theme, tc.Function.Name, tc.Function.Arguments)
				} else {
					fmt.Fprintf(ncw, "› %s\n", tc.Function.Name)
				}
			}

			mac := &multiAgentContext{
				AgentContext: ma.BaseAgent,
				ma:           ma,
			}
			output, toolErr := ma.BaseAgent.Registry.Execute(mac, tc.Function.Name, tc.Function.Arguments)

			if toolErr != nil {
				output = FormatToolExecutionFailure(tc.Function.Name, output, toolErr)
			}
			if output == "" {
				output = "(no output)"
			}

			if tc.Function.Name == "write" || tc.Function.Name == "edit" {
				callCounts = make(map[string]int)
			} else {
				callKey := tc.Function.Name + ":" + strings.TrimSpace(tc.Function.Arguments)
				callCounts[callKey]++
				if callCounts[callKey] >= 2 && toolErr == nil {
					output += "\n\n[Notice: You have inspected this target multiple times with identical arguments. The content has not changed. If no changes are needed, conclude your response or proceed to write/edit.]"
				}
			}

			if !isSubagent && len(assistantMsg.ToolCalls) == 1 {
				sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
			}

			if !isSubagent {
				if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
					ma.BaseAgent.UI.RenderToolOutput(ncw, output, toolErr != nil, ma.BaseAgent.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, sr.DidStreamToolBody(idx))
				} else {
					fmt.Fprintln(ncw, output)
				}
			}

			ma.HistoryMu.Lock()
			ma.History = append(ma.History, db.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    output,
			})
			ma.HistoryMu.Unlock()
			if ma.Manager != nil {
				_ = ma.Manager.SaveAgentState(ma, "running")
			}
		}
	}

	return db.Message{}, fmt.Errorf("reached maximum reasoning steps limit (%d)", maxSteps)
}

type subagentExecutor struct {
	subagent *MultiAgent
	def      tool.Tool
}

func (s *subagentExecutor) Name() string          { return s.def.Function.Name }
func (s *subagentExecutor) Definition() tool.Tool { return s.def }
func (s *subagentExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	prompt := strings.TrimSpace(arguments)
	if strings.HasPrefix(prompt, "\"") && strings.HasSuffix(prompt, "\"") && len(prompt) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(prompt), &unquoted); err == nil {
			prompt = unquoted
		}
	} else if strings.HasPrefix(prompt, "{") {
		var args struct {
			Prompt  string `json:"prompt"`
			Task    string `json:"task"`
			Input   string `json:"input"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(prompt), &args); err == nil {
			if args.Prompt != "" {
				prompt = args.Prompt
			} else if args.Task != "" {
				prompt = args.Task
			} else if args.Input != "" {
				prompt = args.Input
			} else if args.Message != "" {
				prompt = args.Message
			}
		}
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("missing required argument: prompt")
	}

	if s.subagent.Manager == nil {
		return "", fmt.Errorf("subagent '%s' has no manager reference", s.subagent.Name)
	}

	taskID := fmt.Sprintf("subtask_%s", db.NewUUID()[:8])
	s.subagent.Manager.RegisterTask(taskID, s.subagent.Name, prompt)

	select {
	case <-ctx.Context().Done():
		s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
		return "", ctx.Context().Err()
	case <-s.subagent.Context.Done():
		errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
		s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errSub)
		return "", errSub
	case s.subagent.Input <- db.Message{
		Role:       "user",
		Name:       "ParentAgent",
		ToolCallID: taskID,
		Content:    prompt,
	}:
	}

	timeout := 10 * time.Minute // robust default timeout
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeoutChan := time.After(timeout)

	for {
		select {
		case <-ctx.Context().Done():
			s.subagent.CancelActiveTurn()
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
			return "", ctx.Context().Err()
		case <-s.subagent.Context.Done():
			errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errSub)
			return "", errSub
		case <-timeoutChan:
			s.subagent.CancelActiveTurn()
			errTimeout := fmt.Errorf("subagent '%s' execution timed out after %v", s.subagent.Name, timeout)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errTimeout)
			return "", errTimeout
		case <-ticker.C:
			task, err := s.subagent.Manager.GetTask(taskID)
			if err != nil {
				return "", err
			}
			if task.Status == "completed" {
				if len(task.Response) > 10000 {
					truncatedResponse := task.Response[:10000] + fmt.Sprintf("\n\n... [Response truncated: subagent returned %d characters. To prevent context overflow, output is capped at 10000 characters. If you need the full detailed report, please instruct the subagent to write its response directly to a file on disk.]", len(task.Response))
					return truncatedResponse, nil
				}
				return task.Response, nil
			}
			if task.Status == "failed" {
				return "", fmt.Errorf("subagent execution failed: %s", task.Error)
			}
		}
	}
}

type SubagentTask struct {
	ID        string    `json:"id"`
	AgentName string    `json:"agent_name"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"` // "pending", "running", "completed", "failed"
	Response  string    `json:"response"`
	Error     string    `json:"error"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentState struct {
	Status  string       `json:"status"` // "idle", "running", "completed", "failed"
	History []db.Message `json:"history"`
}

type MultiAgentManager struct {
	BaseAgent   *Agent
	Agents      map[string]*MultiAgent
	ActiveAgent *MultiAgent
	mu          sync.RWMutex
	w           io.Writer
	theme       style.UITheme
	agentsDir   string
	Tasks       map[string]*SubagentTask
	TasksMu     sync.RWMutex
}

func NewMultiAgentManager(baseAgent *Agent, w io.Writer, theme style.UITheme) *MultiAgentManager {
	mam := &MultiAgentManager{
		BaseAgent: baseAgent,
		Agents:    make(map[string]*MultiAgent),
		Tasks:     make(map[string]*SubagentTask),
		w:         w,
		theme:     theme,
	}

	if baseAgent != nil && baseAgent.Registry != nil {
		baseAgent.Registry.Register(&spawnSubagentTool{mam: mam})
		baseAgent.Registry.Register(&removeSubagentTool{mam: mam})
		baseAgent.Registry.Register(&swarmTopologyTool{mam: mam})
		baseAgent.Registry.Register(&swarmAuditTool{mam: mam})
	}

	return mam
}

func (mam *MultiAgentManager) RegisterTask(id string, agentName string, prompt string) {
	mam.TasksMu.Lock()
	defer mam.TasksMu.Unlock()
	if mam.Tasks == nil {
		mam.Tasks = make(map[string]*SubagentTask)
	}
	mam.Tasks[id] = &SubagentTask{
		ID:        id,
		AgentName: agentName,
		Prompt:    prompt,
		Status:    "pending",
		UpdatedAt: time.Now(),
	}
}

func (mam *MultiAgentManager) GetTask(id string) (*SubagentTask, error) {
	mam.TasksMu.RLock()
	defer mam.TasksMu.RUnlock()
	task, exists := mam.Tasks[id]
	if !exists {
		return nil, fmt.Errorf("task '%s' not found", id)
	}
	taskCopy := *task
	return &taskCopy, nil
}

func (mam *MultiAgentManager) UpdateTaskStatus(id string, status string, response string, err error) {
	mam.TasksMu.Lock()
	defer mam.TasksMu.Unlock()
	if mam.Tasks == nil {
		return
	}
	task, exists := mam.Tasks[id]
	if !exists {
		return
	}
	task.Status = status
	task.Response = response
	if err != nil {
		task.Error = err.Error()
	}
	task.UpdatedAt = time.Now()
}

func (mam *MultiAgentManager) SaveAgentState(ma *MultiAgent, status string) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return err
	}

	ma.HistoryMu.RLock()
	state := AgentState{
		Status:  status,
		History: ma.History,
	}
	ma.HistoryMu.RUnlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+"_state.json")
	return os.WriteFile(path, data, 0644)
}

func (mam *MultiAgentManager) LoadAgentState(ma *MultiAgent) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+"_state.json")
	if _, err := os.Stat(path); err != nil {
		return nil // No state file yet
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	ma.HistoryMu.Lock()
	if len(state.History) > 0 {
		ma.History = state.History
	}
	ma.HistoryMu.Unlock()
	return nil
}

func (mam *MultiAgentManager) getAgentsDir() (string, error) {
	if mam.agentsDir != "" {
		return mam.agentsDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".maquis", "agents"), nil
}

func validateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("agent name exceeds 64 characters")
	}
	for i, r := range name {
		isAlphaNumeric := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
		if i == 0 && !isAlphaNumeric {
			return fmt.Errorf("agent name must start with a letter or number")
		}
		if !isAlphaNumeric && r != '_' && r != '-' {
			return fmt.Errorf("agent name may contain only letters, numbers, underscores, and hyphens")
		}
	}
	return nil
}

func validateAgentLocalSkill(skill tool.Skill) (tool.Skill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Content = strings.TrimSpace(skill.Content)
	skill.Path = ""

	if err := validateAgentName(skill.Name); err != nil {
		return tool.Skill{}, fmt.Errorf("invalid agent-local skill name %q: %w", skill.Name, err)
	}
	if skill.Content == "" {
		return tool.Skill{}, fmt.Errorf("agent-local skill '%s' has no instructions", skill.Name)
	}
	if len(skill.Description) > 2048 {
		return tool.Skill{}, fmt.Errorf("agent-local skill '%s' description exceeds 2048 bytes", skill.Name)
	}
	if len(skill.Content) > 128*1024 {
		return tool.Skill{}, fmt.Errorf("agent-local skill '%s' instructions exceed 128 KiB", skill.Name)
	}
	if skill.Description == "" {
		skill.Description = "Agent-local specialization."
	}
	return skill, nil
}

func (mam *MultiAgentManager) SpawnAgent(name string, systemPrompt string, parentName string, skillNames []string) error {
	return mam.spawnAgent(name, systemPrompt, parentName, skillNames, nil, len(skillNames) == 0)
}

func (mam *MultiAgentManager) spawnAgent(
	name string,
	systemPrompt string,
	parentName string,
	skillNames []string,
	localSkills []tool.Skill,
	inheritAllSkills bool,
) error {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	name = strings.TrimSpace(name)
	systemPrompt = strings.TrimSpace(systemPrompt)
	parentName = strings.TrimSpace(parentName)

	if err := validateAgentName(name); err != nil {
		return err
	}
	if systemPrompt == "" {
		return fmt.Errorf("system prompt cannot be empty")
	}
	if len(systemPrompt) > 256*1024 {
		return fmt.Errorf("system prompt exceeds 256 KiB")
	}
	if len(localSkills) > 32 {
		return fmt.Errorf("an agent cannot have more than 32 agent-local skills")
	}
	if mam.BaseAgent == nil || mam.BaseAgent.Registry == nil {
		return fmt.Errorf("multi-agent manager is not attached to an initialized base agent")
	}
	if _, exists := mam.Agents[name]; exists {
		return fmt.Errorf("agent '%s' already exists", name)
	}

	var parent *MultiAgent
	if parentName != "" {
		p, exists := mam.Agents[parentName]
		if !exists {
			return fmt.Errorf("parent agent '%s' not found", parentName)
		}
		parent = p
	}

	mam.BaseAgent.ReloadSkills()

	referenceSkills := make([]tool.Skill, 0, len(skillNames))
	assignedNames := make(map[string]struct{})
	if inheritAllSkills {
		referenceSkills = append(referenceSkills, mam.BaseAgent.ActiveSkills...)
		for _, skill := range referenceSkills {
			assignedNames[strings.ToLower(skill.Name)] = struct{}{}
		}
	} else {
		for _, sn := range skillNames {
			sn = strings.TrimSpace(sn)
			if sn == "" {
				return fmt.Errorf("reference skill name cannot be empty")
			}
			var matched *tool.Skill
			for _, s := range mam.BaseAgent.ActiveSkills {
				if strings.EqualFold(s.Name, sn) {
					skillCopy := s
					matched = &skillCopy
					break
				}
			}
			if matched == nil {
				available := make([]string, 0, len(mam.BaseAgent.ActiveSkills))
				for _, skill := range mam.BaseAgent.ActiveSkills {
					available = append(available, skill.Name)
				}
				sort.Strings(available)
				if len(available) == 0 {
					return fmt.Errorf("reference skill '%s' not found; no reference skills are installed", sn)
				}
				return fmt.Errorf("reference skill '%s' not found; available skills: %s", sn, strings.Join(available, ", "))
			}
			key := strings.ToLower(matched.Name)
			if _, exists := assignedNames[key]; exists {
				continue
			}
			assignedNames[key] = struct{}{}
			referenceSkills = append(referenceSkills, *matched)
		}
	}

	normalizedLocalSkills := make([]tool.Skill, 0, len(localSkills))
	for _, localSkill := range localSkills {
		normalized, err := validateAgentLocalSkill(localSkill)
		if err != nil {
			return err
		}
		key := strings.ToLower(normalized.Name)
		if _, exists := assignedNames[key]; exists {
			return fmt.Errorf("skill '%s' is assigned more than once", normalized.Name)
		}
		assignedNames[key] = struct{}{}
		normalizedLocalSkills = append(normalizedLocalSkills, normalized)
	}

	ma := newMultiAgentWithSkills(
		name,
		systemPrompt,
		parent,
		mam.BaseAgent,
		referenceSkills,
		normalizedLocalSkills,
		inheritAllSkills,
	)
	ma.Manager = mam

	if err := mam.LoadAgentState(ma); err != nil {
		return fmt.Errorf("load saved state for agent '%s': %w", name, err)
	}
	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("persist agent '%s': %w", name, err)
	}

	mam.Agents[name] = ma
	mam.BaseAgent.SpawnedAgentsMu.Lock()
	if mam.BaseAgent.SpawnedAgents == nil {
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
	}
	mam.BaseAgent.SpawnedAgents[name] = true
	mam.BaseAgent.SpawnedAgentsMu.Unlock()

	ma.Start(mam.w, mam.theme)

	if parent != nil {
		parent.SubagentsMu.Lock()
		parent.Subagents[name] = ma
		parent.SubagentsMu.Unlock()
	}

	toolName := fmt.Sprintf("subagent__%s", name)
	toolDef := tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        toolName,
			Description: fmt.Sprintf("Delegate a sub-task or ask a question to agent '%s'. Prompt must be clear and specific.", name),
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"prompt": {
						Type:        "string",
						Description: "The specific prompt or instruction for the agent",
					},
				},
				Required: []string{"prompt"},
			},
		},
	}
	mam.BaseAgent.Registry.Register(&subagentExecutor{
		subagent: ma,
		def:      toolDef,
	})

	return nil
}

func (mam *MultiAgentManager) SendMessage(name string, content string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	ma.Input <- db.Message{
		Role:    "user",
		Name:    "User",
		Content: content,
	}
	return nil
}

func (mam *MultiAgentManager) ListAgents() []string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()

	var names []string
	for k := range mam.Agents {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (mam *MultiAgentManager) RemoveAgent(name string) error {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	ma, exists := mam.Agents[name]
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	ma.Cancel()
	delete(mam.Agents, name)

	if mam.BaseAgent != nil {
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		if mam.BaseAgent.SpawnedAgents != nil {
			delete(mam.BaseAgent.SpawnedAgents, name)
		}
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}

	if ma.Parent != nil {
		ma.Parent.SubagentsMu.Lock()
		delete(ma.Parent.Subagents, name)
		ma.Parent.SubagentsMu.Unlock()
	}

	toolName := fmt.Sprintf("subagent__%s", name)
	mam.BaseAgent.Registry.UnregisterPrefix(toolName)

	if mam.ActiveAgent == ma {
		if ma.Parent != nil {
			mam.ActiveAgent = ma.Parent
		} else {
			mam.ActiveAgent = nil
		}
	}

	_ = mam.deleteAgentDef(name)
	return nil
}

func (mam *MultiAgentManager) ActiveAgentName() string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	if mam.ActiveAgent == nil {
		return ""
	}
	return mam.ActiveAgent.Name
}

func (mam *MultiAgentManager) HasAgent(name string) bool {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	_, exists := mam.Agents[name]
	return exists
}

func (mam *MultiAgentManager) GetParentName(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	ag, exists := mam.Agents[name]
	if !exists || ag.Parent == nil {
		return ""
	}
	return ag.Parent.Name
}

func (mam *MultiAgentManager) GetAgentSystemPrompt(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	ag, exists := mam.Agents[name]
	if !exists {
		return ""
	}
	return ag.SystemPrompt
}

func (mam *MultiAgentManager) JoinAgent(name string) bool {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	if name == "base" || name == "main" || name == "" {
		mam.ActiveAgent = nil
		return true
	}
	ag, exists := mam.Agents[name]
	if exists {
		mam.ActiveAgent = ag
		return true
	}
	return false
}

type multiAgentContext struct {
	tool.AgentContext
	ma *MultiAgent
}

func (mac *multiAgentContext) Context() context.Context {
	mac.ma.HistoryMu.RLock()
	ctx := mac.ma.ActiveContext
	mac.ma.HistoryMu.RUnlock()
	if ctx != nil {
		return ctx
	}
	return mac.ma.Context
}

func (mac *multiAgentContext) GetActiveSkills() []tool.Skill {
	mac.ma.HistoryMu.RLock()
	defer mac.ma.HistoryMu.RUnlock()
	return mac.ma.Skills
}

func (mac *multiAgentContext) ReloadSkills() []tool.Skill {
	reloaded := mac.AgentContext.ReloadSkills()
	mac.ma.HistoryMu.Lock()
	localNames := make(map[string]struct{}, len(mac.ma.LocalSkills))
	for _, localSkill := range mac.ma.LocalSkills {
		localNames[strings.ToLower(localSkill.Name)] = struct{}{}
	}
	if mac.ma.HasAllSkills {
		mac.ma.Skills = make([]tool.Skill, 0, len(reloaded)+len(mac.ma.LocalSkills))
		mac.ma.Skills = append(mac.ma.Skills, reloaded...)
		mac.ma.Skills = append(mac.ma.Skills, mac.ma.LocalSkills...)
	} else {
		for i, existing := range mac.ma.Skills {
			if _, isLocal := localNames[strings.ToLower(existing.Name)]; isLocal {
				continue
			}
			for _, r := range reloaded {
				if r.Name == existing.Name {
					mac.ma.Skills[i] = r
					break
				}
			}
		}
	}
	mac.ma.HistoryMu.Unlock()
	return reloaded
}

func (mam *MultiAgentManager) ListAgentSkills(name string) ([]tool.Skill, error) {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("agent '%s' not found", name)
	}

	ma.HistoryMu.RLock()
	defer ma.HistoryMu.RUnlock()
	skills := make([]tool.Skill, len(ma.Skills))
	copy(skills, ma.Skills)
	return skills, nil
}

func (mam *MultiAgentManager) LoadAgentSkill(agentName string, skillName string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[agentName]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	var targetSkill *tool.Skill
	for _, s := range mam.BaseAgent.ActiveSkills {
		if s.Name == skillName {
			targetSkill = &s
			break
		}
	}
	if targetSkill == nil {
		return fmt.Errorf("reference skill '%s' not found in config skills directory", skillName)
	}

	ma.HistoryMu.Lock()
	alreadyHas := false
	for _, s := range ma.Skills {
		if s.Name == skillName {
			alreadyHas = true
			break
		}
	}
	if !alreadyHas {
		ma.Skills = append(ma.Skills, *targetSkill)
	}

	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", targetSkill.Name, targetSkill.Content),
	})
	ma.HistoryMu.Unlock()

	_ = mam.saveAgentDef(ma)
	return nil
}

func (mam *MultiAgentManager) ClearAgentSkills(agentName string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[agentName]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	ma.HistoryMu.Lock()
	ma.Skills = []tool.Skill{}
	ma.LocalSkills = nil
	ma.HasAllSkills = false
	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: "cleared all loaded reference skills.",
	})
	ma.HistoryMu.Unlock()

	_ = mam.saveAgentDef(ma)
	return nil
}

type AgentDef struct {
	Name             string       `json:"name"`
	SystemPrompt     string       `json:"system_prompt"`
	ParentName       string       `json:"parent_name"`
	SkillNames       []string     `json:"skill_names"`
	LocalSkills      []tool.Skill `json:"local_skills,omitempty"`
	InheritAllSkills *bool        `json:"inherit_all_skills,omitempty"`
}

func (mam *MultiAgentManager) saveAgentDef(ma *MultiAgent) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return err
	}

	var skillNames []string
	ma.HistoryMu.RLock()
	localNames := make(map[string]struct{}, len(ma.LocalSkills))
	for _, localSkill := range ma.LocalSkills {
		localNames[strings.ToLower(localSkill.Name)] = struct{}{}
	}
	for _, s := range ma.Skills {
		if _, isLocal := localNames[strings.ToLower(s.Name)]; isLocal {
			continue
		}
		skillNames = append(skillNames, s.Name)
	}
	localSkills := append([]tool.Skill(nil), ma.LocalSkills...)
	inheritAllSkills := ma.HasAllSkills
	ma.HistoryMu.RUnlock()

	parentName := ""
	if ma.Parent != nil {
		parentName = ma.Parent.Name
	}

	if inheritAllSkills {
		skillNames = nil
	}
	def := AgentDef{
		Name:             ma.Name,
		SystemPrompt:     ma.SystemPrompt,
		ParentName:       parentName,
		SkillNames:       skillNames,
		LocalSkills:      localSkills,
		InheritAllSkills: &inheritAllSkills,
	}

	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func (mam *MultiAgentManager) deleteAgentDef(name string) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, name+".json")
	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}

func (mam *MultiAgentManager) LoadSavedAgents() error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(agentsDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return err
	}

	defs := make(map[string]*AgentDef)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || strings.HasSuffix(entry.Name(), "_state.json") {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var def AgentDef
		if err := json.Unmarshal(data, &def); err == nil && def.Name != "" {
			defs[def.Name] = &def
		}
	}

	var spawn func(name string) error
	spawned := make(map[string]bool)
	spawning := make(map[string]bool)

	spawn = func(name string) error {
		if spawned[name] {
			return nil
		}
		if spawning[name] {
			return fmt.Errorf("circular dependency detected for agent '%s'", name)
		}
		spawning[name] = true
		defer func() { spawning[name] = false }()

		def, exists := defs[name]
		if !exists {
			return fmt.Errorf("agent definition not found for '%s'", name)
		}

		if def.ParentName != "" {
			if err := spawn(def.ParentName); err != nil {
				return err
			}
		}

		inheritAllSkills := def.SkillNames == nil
		if def.InheritAllSkills != nil {
			inheritAllSkills = *def.InheritAllSkills
		}
		err := mam.spawnAgent(
			name,
			def.SystemPrompt,
			def.ParentName,
			def.SkillNames,
			def.LocalSkills,
			inheritAllSkills,
		)
		if err != nil {
			return err
		}

		spawned[name] = true
		return nil
	}

	for name := range defs {
		_ = spawn(name)
	}

	mam.mu.Lock()
	mam.ActiveAgent = nil
	mam.mu.Unlock()

	return nil
}

// Below are the implementations for the dynamic multi-agent tools:

type spawnSubagentTool struct {
	mam *MultiAgentManager
}

type inlineSkillRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

func (s *spawnSubagentTool) Name() string { return "spawn_subagent" }
func (s *spawnSubagentTool) Definition() tool.Tool {
	availableSkillNames := []string{}
	if s != nil && s.mam != nil && s.mam.BaseAgent != nil {
		seen := make(map[string]struct{}, len(s.mam.BaseAgent.ActiveSkills))
		for _, skill := range s.mam.BaseAgent.ActiveSkills {
			name := strings.TrimSpace(skill.Name)
			key := strings.ToLower(name)
			if name == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			availableSkillNames = append(availableSkillNames, name)
		}
		sort.Strings(availableSkillNames)
	}

	skillNamesDescription := "Optional exact names of registered reference skills."
	if len(availableSkillNames) == 0 {
		skillNamesDescription += " No reference skills are currently registered. Omit this field or use inline_skills."
	} else {
		skillNamesDescription += " Available names: " + strings.Join(availableSkillNames, ", ") + "."
	}
	skillNamesDescription += " Unknown names are converted into agent-local skills using system_prompt so spawning can continue."

	return tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        "spawn_subagent",
			Description: "Spawn one specialized subagent. Use system_prompt for its role. Assign exact registered skill_names or define new private inline_skills; never invent a registered skill name.",
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"name": {
						Type:        "string",
						Description: "Unique name for the subagent (e.g. 'coder', 'researcher').",
					},
					"system_prompt": {
						Type:        "string",
						Description: "The specific role, instructions, and goals for this subagent.",
					},
					"skill_names": {
						Type:        "array",
						Description: skillNamesDescription,
						Items: &tool.SchemaProp{
							Type: "string",
							Enum: availableSkillNames,
						},
					},
					"inline_skills": {
						Type:        "array",
						Description: "Optional private skills for this subagent. Use these when the requested specialization is not a registered reference skill.",
						Items: &tool.SchemaProp{
							Type: "object",
							Properties: map[string]tool.SchemaProp{
								"name": {
									Type:        "string",
									Description: "Unique skill name using letters, numbers, underscores, or hyphens.",
								},
								"description": {
									Type:        "string",
									Description: "Short summary of the specialization.",
								},
								"instructions": {
									Type:        "string",
									Description: "Complete operational instructions for this skill.",
								},
							},
							Required: []string{"name", "instructions"},
						},
					},
				},
				Required: []string{"name", "system_prompt"},
			},
		},
	}
}

func (s *spawnSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	var args struct {
		Name         string               `json:"name"`
		AgentName    string               `json:"agent_name"`
		SubagentName string               `json:"subagent_name"`
		SystemPrompt string               `json:"system_prompt"`
		Instructions string               `json:"instructions"`
		Prompt       string               `json:"prompt"`
		Role         string               `json:"role"`
		SkillNames   []string             `json:"skill_names"`
		Skills       []string             `json:"skills"`
		InlineSkills []inlineSkillRequest `json:"inline_skills"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" {
		if args.AgentName != "" {
			args.Name = args.AgentName
		} else if args.SubagentName != "" {
			args.Name = args.SubagentName
		}
	}
	if args.SystemPrompt == "" {
		if args.Instructions != "" {
			args.SystemPrompt = args.Instructions
		} else if args.Prompt != "" {
			args.SystemPrompt = args.Prompt
		} else if args.Role != "" {
			args.SystemPrompt = args.Role
		}
	}
	if len(args.SkillNames) == 0 && len(args.Skills) > 0 {
		args.SkillNames = args.Skills
	}
	if strings.TrimSpace(args.Name) == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	if strings.TrimSpace(args.SystemPrompt) == "" {
		return "", fmt.Errorf("missing required argument: system_prompt")
	}

	if s == nil || s.mam == nil || s.mam.BaseAgent == nil {
		return "", fmt.Errorf("spawn_subagent is not attached to an initialized multi-agent manager")
	}

	var parentName string
	if mac, ok := ctx.(*multiAgentContext); ok {
		parentName = mac.ma.Name
	}

	s.mam.BaseAgent.ReloadSkills()
	availableSkills := make(map[string]string, len(s.mam.BaseAgent.ActiveSkills))
	for _, skill := range s.mam.BaseAgent.ActiveSkills {
		availableSkills[strings.ToLower(strings.TrimSpace(skill.Name))] = skill.Name
	}

	localSkills := make([]tool.Skill, 0, len(args.InlineSkills)+len(args.SkillNames))
	localNames := make(map[string]struct{}, len(args.InlineSkills)+len(args.SkillNames))
	for _, inlineSkill := range args.InlineSkills {
		name := strings.TrimSpace(inlineSkill.Name)
		key := strings.ToLower(name)
		if _, exists := localNames[key]; exists {
			return "", fmt.Errorf("agent-local skill '%s' is specified more than once", name)
		}
		localNames[key] = struct{}{}
		localSkills = append(localSkills, tool.Skill{
			Name:        name,
			Description: inlineSkill.Description,
			Content:     inlineSkill.Instructions,
		})
	}

	referenceSkillNames := make([]string, 0, len(args.SkillNames))
	referenceNamesSeen := make(map[string]struct{}, len(args.SkillNames))
	convertedSkillNames := []string{}
	for _, requestedName := range args.SkillNames {
		requestedName = strings.TrimSpace(requestedName)
		key := strings.ToLower(requestedName)
		if canonicalName, exists := availableSkills[key]; exists {
			canonicalKey := strings.ToLower(canonicalName)
			if _, duplicate := referenceNamesSeen[canonicalKey]; !duplicate {
				referenceNamesSeen[canonicalKey] = struct{}{}
				referenceSkillNames = append(referenceSkillNames, canonicalName)
			}
			continue
		}
		if _, alreadyInline := localNames[key]; alreadyInline {
			continue
		}
		localNames[key] = struct{}{}
		convertedSkillNames = append(convertedSkillNames, requestedName)
		localSkills = append(localSkills, tool.Skill{
			Name:        requestedName,
			Description: "Agent-local specialization derived from the subagent role.",
			Content:     args.SystemPrompt,
		})
	}

	inheritAllSkills := args.SkillNames == nil && args.InlineSkills == nil
	err := s.mam.spawnAgent(
		args.Name,
		args.SystemPrompt,
		parentName,
		referenceSkillNames,
		localSkills,
		inheritAllSkills,
	)
	if err != nil {
		return "", err
	}

	var assignmentSummary []string
	if len(referenceSkillNames) > 0 {
		assignmentSummary = append(assignmentSummary, "reference skills: "+strings.Join(referenceSkillNames, ", "))
	}
	if len(localSkills) > 0 {
		localSkillNames := make([]string, 0, len(localSkills))
		for _, localSkill := range localSkills {
			localSkillNames = append(localSkillNames, localSkill.Name)
		}
		assignmentSummary = append(assignmentSummary, "agent-local skills: "+strings.Join(localSkillNames, ", "))
	}

	result := fmt.Sprintf("Subagent '%s' successfully spawned", args.Name)
	if len(assignmentSummary) > 0 {
		result += " with " + strings.Join(assignmentSummary, "; ")
	}
	result += fmt.Sprintf(". Use 'subagent__%s' to delegate tasks to it.", args.Name)
	if len(convertedSkillNames) > 0 {
		result += " Unregistered skill labels were converted to persistent agent-local skills: " + strings.Join(convertedSkillNames, ", ") + "."
	}
	return result, nil
}

func parseSubagentName(arguments string) (string, error) {
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			trimmed = unquoted
		}
	} else if strings.HasPrefix(trimmed, "{") {
		var args struct {
			Name         string `json:"name"`
			AgentName    string `json:"agent_name"`
			SubagentName string `json:"subagent_name"`
			Agent        string `json:"agent"`
			Subagent     string `json:"subagent"`
		}
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Name != "" {
			trimmed = args.Name
		} else if args.AgentName != "" {
			trimmed = args.AgentName
		} else if args.SubagentName != "" {
			trimmed = args.SubagentName
		} else if args.Agent != "" {
			trimmed = args.Agent
		} else if args.Subagent != "" {
			trimmed = args.Subagent
		}
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	return trimmed, nil
}

type removeSubagentTool struct {
	mam *MultiAgentManager
}

func (s *removeSubagentTool) Name() string { return "remove_subagent" }
func (s *removeSubagentTool) Definition() tool.Tool {
	return tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        "remove_subagent",
			Description: "Terminate a running subagent.",
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"name": {
						Type:        "string",
						Description: "The name of the subagent to terminate.",
					},
				},
				Required: []string{"name"},
			},
		},
	}
}

func (s *removeSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}

	err = s.mam.RemoveAgent(name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Subagent '%s' terminated.", name), nil
}

type swarmTopologyTool struct {
	mam *MultiAgentManager
}

func (s *swarmTopologyTool) Name() string { return "swarm_topology" }
func (s *swarmTopologyTool) Definition() tool.Tool {
	return tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        "swarm_topology",
			Description: "View the tree hierarchy of all active spawned subagents and their loaded skills.",
			Parameters: tool.JSONSchema{
				Type:       "object",
				Properties: map[string]tool.SchemaProp{},
			},
		},
	}
}

func (s *swarmTopologyTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	agents := s.mam.ListAgents()
	if len(agents) == 0 {
		return "No subagents currently spawned in the swarm.", nil
	}
	var sb strings.Builder
	sb.WriteString("Active Swarm Topology:\n")
	for _, name := range agents {
		parent := s.mam.GetParentName(name)
		skills, _ := s.mam.ListAgentSkills(name)
		var skillNames []string
		for _, sk := range skills {
			skillNames = append(skillNames, sk.Name)
		}
		skillStr := "None"
		if len(skillNames) > 0 {
			skillStr = strings.Join(skillNames, ", ")
		}
		parentStr := "Base Agent"
		if parent != "" {
			parentStr = parent
		}
		sb.WriteString(fmt.Sprintf("- %s (Parent: %s) [Skills: %s]\n", name, parentStr, skillStr))
		sysPrompt := s.mam.GetAgentSystemPrompt(name)
		if len(sysPrompt) > 100 {
			sysPrompt = sysPrompt[:97] + "..."
		}
		sysPrompt = strings.ReplaceAll(sysPrompt, "\n", " ")
		sb.WriteString(fmt.Sprintf("  Goal: %s\n", sysPrompt))
	}
	return sb.String(), nil
}

type swarmAuditTool struct {
	mam *MultiAgentManager
}

func (s *swarmAuditTool) Name() string { return "swarm_audit" }
func (s *swarmAuditTool) Definition() tool.Tool {
	return tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        "swarm_audit",
			Description: "Audit the execution history of a spawned subagent to see exactly what actions, tool calls, thoughts, and results it produced. Essential for verifying subagent work.",
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"name": {
						Type:        "string",
						Description: "The name of the subagent to audit (e.g. 'coder', 'researcher').",
					},
				},
				Required: []string{"name"},
			},
		},
	}
}

func (s *swarmAuditTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}

	s.mam.mu.RLock()
	subagent, exists := s.mam.Agents[name]
	s.mam.mu.RUnlock()

	if !exists {
		// Try loading state from disk if it exists but not in memory
		agentsDir, err := s.mam.getAgentsDir()
		if err != nil {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}
		path := filepath.Join(agentsDir, name+"_state.json")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}

		// Create a dummy MultiAgent just to load its state
		subagent = &MultiAgent{Name: name}
		if err := s.mam.LoadAgentState(subagent); err != nil {
			return "", fmt.Errorf("failed to load subagent state: %w", err)
		}
	}

	subagent.HistoryMu.RLock()
	history := make([]db.Message, len(subagent.History))
	copy(history, subagent.History)
	subagent.HistoryMu.RUnlock()

	if len(history) == 0 {
		return fmt.Sprintf("No execution history found for subagent '%s'.", name), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Swarm Audit Trail for Subagent: '%s' ===\n\n", name))

	step := 1
	for _, msg := range history {
		if msg.Role == "system" {
			// Skip system prompt to avoid cluttering unless it's a loaded skill
			if strings.HasPrefix(msg.Content, "loaded reference skill") {
				sb.WriteString(fmt.Sprintf("[System] %s\n\n", msg.Content))
			}
			continue
		}

		if msg.Role == "user" {
			sb.WriteString(fmt.Sprintf("Step %d: [Task Assigned from %s]\n", step, msg.Name))
			sb.WriteString(fmt.Sprintf("Prompt: %s\n\n", msg.Content))
			step++
			continue
		}

		if msg.Role == "assistant" {
			if msg.ReasoningContent != "" {
				sb.WriteString(fmt.Sprintf("Thought:\n%s\n\n", msg.ReasoningContent))
			}
			if msg.Content != "" {
				sb.WriteString(fmt.Sprintf("Response:\n%s\n\n", msg.Content))
			}
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					sb.WriteString(fmt.Sprintf("Action (Tool Call): %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
			continue
		}

		if msg.Role == "tool" {
			out := msg.Content
			// Truncate very long tool outputs to keep the audit readable
			if len(out) > 1500 {
				out = out[:1500] + "\n... (output truncated for audit readability) ..."
			}
			sb.WriteString(fmt.Sprintf("Result (Tool Output - %s):\n%s\n\n", msg.Name, out))
			continue
		}
	}

	return sb.String(), nil
}
func (mam *MultiAgentManager) ClearAllAgents() {
	mam.mu.Lock()
	for _, ma := range mam.Agents {
		ma.CancelActiveTurn()
	}
	mam.Agents = make(map[string]*MultiAgent)
	mam.mu.Unlock()

	// Wipe all JSON state files
	home, err := os.UserHomeDir()
	if err == nil {
		os.RemoveAll(filepath.Join(home, ".maquis", "agents"))
	}

	// Unregister tools from the base agent
	if mam.BaseAgent != nil {
		mam.BaseAgent.Registry.UnregisterPrefix("subagent__")

		mam.BaseAgent.SpawnedAgentsMu.Lock()
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}
}

func (mam *MultiAgentManager) RenderStats(w io.Writer, baseMessages []db.Message, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	valueStyle := style.NewStyle().Foreground(theme.Text)

	calcTokens := func(history []db.Message) (int, int) {
		var prompt, completion int
		for _, m := range history {
			if m.Role == "assistant" {
				prompt += m.PromptTokens
				completion += m.CompletionTokens
			}
		}
		return prompt, completion
	}

	fmt.Fprintln(w, headerStyle.Render("╭───────────────────────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  SWARM TOKEN UTILIZATION & COST STATS                                                             │"))
	fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))

	baseP, baseC := calcTokens(baseMessages)
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
	fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
	fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
	fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", baseP+baseC)))

	mam.mu.RLock()
	var names []string
	for name := range mam.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ma := mam.Agents[name]
		ma.HistoryMu.RLock()
		subP, subC := calcTokens(ma.History)
		ma.HistoryMu.RUnlock()

		// Truncate system prompt for clean display
		role := ma.SystemPrompt
		if len(role) > 60 {
			role = role[:57] + "..."
		}
		role = strings.ReplaceAll(role, "\n", " ")

		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Subagent: "+name))
		fmt.Fprintf(w, "    Role/Goal:          %s\n", valueStyle.Render(role))
		fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", subP)))
		fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", subC)))
		fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", subP+subC)))
	}
	mam.mu.RUnlock()

	fmt.Fprintln(w, headerStyle.Render("╰───────────────────────────────────────────────────────────────────────────────────────────────────╯"))
}
