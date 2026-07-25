package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

func TestMultiAgentCancellationScopes(t *testing.T) {
	parentTurn, cancelParent := context.WithCancel(context.Background())
	childTurn, cancelChild := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	t.Cleanup(cancelChild)

	parent := &MultiAgent{
		Name:          "parent",
		ActiveContext: parentTurn,
		ActiveCancel:  cancelParent,
		ActiveStarted: time.Now().Add(-time.Second),
	}
	child := &MultiAgent{
		Name:          "child",
		Parent:        parent,
		ActiveContext: childTurn,
		ActiveCancel:  cancelChild,
		ActiveStarted: time.Now(),
	}
	mam := &MultiAgentManager{
		Agents: map[string]*MultiAgent{
			parent.Name: parent,
			child.Name:  child,
		},
	}

	current, ok := mam.ActiveSubagentName()
	if !ok || current != child.Name {
		t.Fatalf("current active subagent = %q, %t; want %q, true", current, ok, child.Name)
	}
	if !mam.CancelSubagentTurn(current) {
		t.Fatalf("failed to skip current subagent %q", current)
	}
	select {
	case <-childTurn.Done():
	case <-time.After(time.Second):
		t.Fatal("child turn was not cancelled")
	}
	select {
	case <-parentTurn.Done():
		t.Fatal("skipping the child also cancelled its parent")
	default:
	}

	current, ok = mam.ActiveSubagentName()
	if !ok || current != parent.Name {
		t.Fatalf("active subagent after child skip = %q, %t; want %q, true", current, ok, parent.Name)
	}
	cancelled := mam.CancelAllActiveSubagents()
	if len(cancelled) != 1 || cancelled[0] != parent.Name {
		t.Fatalf("stop-all cancelled %v; want [%s]", cancelled, parent.Name)
	}
	select {
	case <-parentTurn.Done():
	case <-time.After(time.Second):
		t.Fatal("parent turn was not cancelled by stop-all")
	}
}

func TestParentCancellationStopsDelegatedSubagentTurn(t *testing.T) {
	parentContext, cancelParent := context.WithCancel(context.Background())
	childLifetime, cancelChildLifetime := context.WithCancel(context.Background())
	childTurn, cancelChildTurn := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	t.Cleanup(cancelChildLifetime)
	t.Cleanup(cancelChildTurn)

	base := &Agent{CurrentContext: parentContext}
	mam := &MultiAgentManager{Tasks: make(map[string]*SubagentTask)}
	subagent := &MultiAgent{
		Name:          "worker",
		Manager:       mam,
		Context:       childLifetime,
		Input:         make(chan db.Message, 1),
		ActiveContext: childTurn,
		ActiveCancel:  cancelChildTurn,
		ActiveStarted: time.Now(),
	}
	executor := &subagentExecutor{subagent: subagent}

	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(base, `{"prompt":"run the audit"}`)
		result <- err
	}()

	select {
	case <-subagent.Input:
	case <-time.After(time.Second):
		t.Fatal("delegated task did not reach the subagent")
	}
	cancelParent()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("delegation returned %v; want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delegation did not return after parent cancellation")
	}
	select {
	case <-childTurn.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation left the subagent turn running")
	}
}

func TestMultiAgentSpawnSkills(t *testing.T) {
	cfg := &config.Config{}
	baseAgent := &Agent{
		Config:   cfg,
		Registry: tool.NewToolRegistry(),
		ActiveSkills: []tool.Skill{
			{Name: "git_guru", Description: "Git expert", Content: "Use git"},
			{Name: "go_test_master", Description: "Go testing", Content: "Use testing"},
		},
	}

	var buf bytes.Buffer
	theme := style.UITheme{}
	mam := NewMultiAgentManager(baseAgent, &buf, theme)
	mam.agentsDir = t.TempDir()

	// Test case 1: Spawning generic agent (empty skillName)
	err := mam.SpawnAgent("generic_bob", "You are bob", "", nil)
	if err != nil {
		t.Fatalf("failed to spawn generic agent: %v", err)
	}

	genericAgent, exists := mam.Agents["generic_bob"]
	if !exists {
		t.Fatalf("generic_bob not found in manager")
	}

	if len(genericAgent.Skills) != 2 {
		t.Errorf("expected generic agent to inherit all 2 skills, got %d", len(genericAgent.Skills))
	}

	// Test case 2: Spawning with a specific skill
	err = mam.SpawnAgent("git_alice", "You are alice", "", []string{"git_guru"})
	if err != nil {
		t.Fatalf("failed to spawn git_alice: %v", err)
	}

	gitAgent, exists := mam.Agents["git_alice"]
	if !exists {
		t.Fatalf("git_alice not found in manager")
	}

	if len(gitAgent.Skills) != 1 {
		t.Errorf("expected git_alice to have exactly 1 skill, got %d", len(gitAgent.Skills))
	} else if gitAgent.Skills[0].Name != "git_guru" {
		t.Errorf("expected git_alice's skill to be git_guru, got %s", gitAgent.Skills[0].Name)
	}

	// Test case 3: Spawning with non-existent skill should fail
	err = mam.SpawnAgent("bad_agent", "You are bad", "", []string{"non_existent_skill"})
	if err == nil {
		t.Fatalf("expected error when spawning with non-existent skill, got nil")
	}

	// Test case 4: Dynamic reloading of skills propagates to subagents
	baseAgent.ActiveSkills = []tool.Skill{
		{Name: "git_guru", Description: "Git expert", Content: "Use git carefully"},
		{Name: "go_test_master", Description: "Go testing", Content: "Use testing"},
		{Name: "docker_deploy", Description: "Docker deployer", Content: "Use docker"},
	}

	macBob := &multiAgentContext{
		AgentContext: baseAgent,
		ma:           genericAgent,
	}
	macBob.ReloadSkills()

	if len(genericAgent.Skills) != 3 {
		t.Errorf("expected genericAgent to have 3 skills after reload, got %d", len(genericAgent.Skills))
	}

	macAlice := &multiAgentContext{
		AgentContext: baseAgent,
		ma:           gitAgent,
	}
	macAlice.ReloadSkills()

	if len(gitAgent.Skills) != 1 {
		t.Errorf("expected gitAgent to still have 1 skill after reload, got %d", len(gitAgent.Skills))
	} else if gitAgent.Skills[0].Content != "Use git carefully" {
		t.Errorf("expected gitAgent's git_guru skill content to be updated, got %q", gitAgent.Skills[0].Content)
	}
}

func TestMultiAgentPersistence(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{}
	baseAgent := &Agent{
		Config:   cfg,
		Registry: tool.NewToolRegistry(),
		ActiveSkills: []tool.Skill{
			{Name: "git_guru", Description: "Git expert", Content: "Use git"},
		},
	}

	var buf bytes.Buffer
	theme := style.UITheme{}
	mam := NewMultiAgentManager(baseAgent, &buf, theme)
	mam.agentsDir = tempDir

	// Spawn parent agent
	err := mam.SpawnAgent("parent_agent", "System Prompt Parent", "", []string{"git_guru"})
	if err != nil {
		t.Fatalf("failed to spawn parent: %v", err)
	}

	// Spawn subagent under parent
	err = mam.SpawnAgent("sub_agent", "System Prompt Sub", "parent_agent", []string{"git_guru"})
	if err != nil {
		t.Fatalf("failed to spawn sub: %v", err)
	}

	// Verify files exist in tempDir
	parentPath := filepath.Join(tempDir, "parent_agent.json")
	subPath := filepath.Join(tempDir, "sub_agent.json")

	if _, err := os.Stat(parentPath); err != nil {
		t.Errorf("expected parent json to exist, got error: %v", err)
	}
	if _, err := os.Stat(subPath); err != nil {
		t.Errorf("expected subagent json to exist, got error: %v", err)
	}

	// Recreate manager and load saved agents
	mam2 := NewMultiAgentManager(baseAgent, &buf, theme)
	mam2.agentsDir = tempDir

	err = mam2.LoadSavedAgents()
	if err != nil {
		t.Fatalf("failed to load saved agents: %v", err)
	}

	// Verify loaded state
	parent2, parentExists := mam2.Agents["parent_agent"]
	if !parentExists {
		t.Fatalf("parent_agent not loaded")
	}
	if parent2.SystemPrompt != "System Prompt Parent" {
		t.Errorf("expected parent prompt to be restored, got %q", parent2.SystemPrompt)
	}

	sub2, subExists := mam2.Agents["sub_agent"]
	if !subExists {
		t.Fatalf("sub_agent not loaded")
	}
	if sub2.Parent != parent2 {
		t.Errorf("expected subagent parent relationship to be restored, got %+v", sub2.Parent)
	}

	// Test deletion on Kill
	err = mam2.RemoveAgent("sub_agent")
	if err != nil {
		t.Fatalf("failed to kill subagent: %v", err)
	}
	if _, err := os.Stat(subPath); !os.IsNotExist(err) {
		t.Errorf("expected subagent json file to be deleted, but it still exists")
	}
}

func TestMultiAgentToolRegistration(t *testing.T) {
	cfg := &config.Config{}
	baseAgent := &Agent{
		Config:   cfg,
		Registry: tool.NewToolRegistry(),
	}

	var buf bytes.Buffer
	theme := style.UITheme{}
	mam := NewMultiAgentManager(baseAgent, &buf, theme)
	mam.agentsDir = t.TempDir()

	// Spawn independent agent (no parent)
	err := mam.SpawnAgent("bob", "You are bob", "", nil)
	if err != nil {
		t.Fatalf("failed to spawn bob: %v", err)
	}

	// Verify the tool subagent__bob is registered in the base agent's registry
	tExecutor, ok := baseAgent.Registry.GetAllExecutors()["subagent__bob"]
	if !ok {
		t.Fatalf("expected tool 'subagent__bob' to be registered in the registry")
	}

	if tExecutor.Name() != "subagent__bob" {
		t.Errorf("expected tool name to be 'subagent__bob', got %q", tExecutor.Name())
	}

	// Kill agent and verify tool is unregistered
	err = mam.RemoveAgent("bob")
	if err != nil {
		t.Fatalf("failed to kill bob: %v", err)
	}

	_, ok = baseAgent.Registry.GetAllExecutors()["subagent__bob"]
	if ok {
		t.Errorf("expected tool 'subagent__bob' to be unregistered after agent was killed")
	}
}

func TestSwarmAuditTool(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{}
	baseAgent := &Agent{
		Config:   cfg,
		Registry: tool.NewToolRegistry(),
	}

	var buf bytes.Buffer
	theme := style.UITheme{}
	mam := NewMultiAgentManager(baseAgent, &buf, theme)
	mam.agentsDir = tempDir

	err := mam.SpawnAgent("bob", "You are bob", "", nil)
	if err != nil {
		t.Fatalf("failed to spawn bob: %v", err)
	}

	bob, exists := mam.Agents["bob"]
	if !exists {
		t.Fatalf("bob not found")
	}

	// Setup mock history on bob
	bob.HistoryMu.Lock()
	bob.History = append(bob.History, db.Message{
		Role:    "user",
		Name:    "ParentAgent",
		Content: "Hello bob, please run tests",
	})
	bob.History = append(bob.History, db.Message{
		Role:             "assistant",
		ReasoningContent: "I need to run the tests to verify the code.",
		Content:          "Running tests now.",
		ToolCalls: []db.ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: db.ToolFunction{
					Name:      "bash",
					Arguments: `{"command":"go test ./..."}`,
				},
			},
		},
	})
	bob.History = append(bob.History, db.Message{
		Role:       "tool",
		ToolCallID: "call_1",
		Name:       "bash",
		Content:    "PASS\nok  maquis/pkg/agent",
	})
	bob.History = append(bob.History, db.Message{
		Role:    "assistant",
		Content: "Tests passed successfully!",
	})
	bob.HistoryMu.Unlock()

	// Save bob's state to tempDir
	err = mam.SaveAgentState(bob, "idle")
	if err != nil {
		t.Fatalf("failed to save bob state: %v", err)
	}

	// Verify swarm_audit tool is registered and works
	auditExecutor, ok := baseAgent.Registry.GetAllExecutors()["swarm_audit"]
	if !ok {
		t.Fatalf("swarm_audit tool not registered")
	}

	res, err := auditExecutor.Execute(baseAgent, `{"name":"bob"}`)
	if err != nil {
		t.Fatalf("failed to execute swarm_audit: %v", err)
	}

	if !strings.Contains(res, "=== Swarm Audit Trail for Subagent: 'bob' ===") {
		t.Errorf("unexpected audit output: %q", res)
	}
	if !strings.Contains(res, "I need to run the tests to verify the code.") {
		t.Errorf("expected thought in audit trail, got: %q", res)
	}
	if !strings.Contains(res, "Action (Tool Call): bash") {
		t.Errorf("expected tool call in audit trail, got: %q", res)
	}
	if !strings.Contains(res, "Result (Tool Output - bash)") {
		t.Errorf("expected tool output in audit trail, got: %q", res)
	}
	if !strings.Contains(res, "Tests passed successfully!") {
		t.Errorf("expected final response in audit trail, got: %q", res)
	}
}

func TestSpawnSubagentToolConvertsUnknownSkillToPersistentLocalSkill(t *testing.T) {
	skillsDir := t.TempDir()
	agentsDir := t.TempDir()
	cfg := &config.Config{
		SkillsDir: skillsDir,
	}
	baseAgent := &Agent{
		Config:        cfg,
		Registry:      tool.NewToolRegistry(),
		SpawnedAgents: make(map[string]bool),
		WorkspaceRoot: t.TempDir(),
	}

	var buf bytes.Buffer
	mam := NewMultiAgentManager(baseAgent, &buf, style.UITheme{})
	mam.agentsDir = agentsDir

	spawnTool := &spawnSubagentTool{mam: mam}
	result, err := spawnTool.Execute(baseAgent, `{
		"name": "coder_agent",
		"system_prompt": "You are responsible for implementation and code quality.",
		"skill_names": ["code_expert"]
	}`)
	if err != nil {
		t.Fatalf("spawn_subagent failed: %v", err)
	}
	if !strings.Contains(result, "agent-local skills: code_expert") {
		t.Fatalf("expected local-skill assignment in result, got %q", result)
	}

	coder, exists := mam.Agents["coder_agent"]
	if !exists {
		t.Fatal("coder_agent was not registered")
	}
	t.Cleanup(coder.Cancel)
	if coder.HasAllSkills {
		t.Error("agent with an explicit local skill must not inherit every reference skill")
	}
	if len(coder.LocalSkills) != 1 || coder.LocalSkills[0].Name != "code_expert" {
		t.Fatalf("unexpected local skills: %+v", coder.LocalSkills)
	}
	if len(coder.Skills) != 1 || coder.Skills[0].Content != "You are responsible for implementation and code quality." {
		t.Fatalf("unexpected effective skills: %+v", coder.Skills)
	}

	loadSkill := tool.NewLoadSkillTool()
	mac := &multiAgentContext{AgentContext: baseAgent, ma: coder}
	loaded, err := loadSkill.Execute(mac, `{"name":"code_expert"}`)
	if err != nil {
		t.Fatalf("agent-local skill could not be loaded: %v", err)
	}
	if !strings.Contains(loaded, "implementation and code quality") {
		t.Fatalf("loaded skill does not contain its instructions: %q", loaded)
	}

	reloadedBase := &Agent{
		Config:        cfg,
		Registry:      tool.NewToolRegistry(),
		SpawnedAgents: make(map[string]bool),
		WorkspaceRoot: baseAgent.WorkspaceRoot,
	}
	reloadedManager := NewMultiAgentManager(reloadedBase, &buf, style.UITheme{})
	reloadedManager.agentsDir = agentsDir
	if err := reloadedManager.LoadSavedAgents(); err != nil {
		t.Fatalf("reload saved agents: %v", err)
	}
	reloadedCoder, exists := reloadedManager.Agents["coder_agent"]
	if !exists {
		t.Fatal("persistent coder_agent was not restored")
	}
	t.Cleanup(reloadedCoder.Cancel)
	if len(reloadedCoder.LocalSkills) != 1 || reloadedCoder.LocalSkills[0].Name != "code_expert" {
		t.Fatalf("persistent local skill was not restored: %+v", reloadedCoder.LocalSkills)
	}
}

func TestSpawnSubagentToolSpawnsFiveAgentsWithDistinctLocalSkills(t *testing.T) {
	baseAgent := &Agent{
		Config: &config.Config{
			SkillsDir: t.TempDir(),
		},
		Registry:      tool.NewToolRegistry(),
		SpawnedAgents: make(map[string]bool),
		WorkspaceRoot: t.TempDir(),
	}
	mam := NewMultiAgentManager(baseAgent, &bytes.Buffer{}, style.UITheme{})
	mam.agentsDir = t.TempDir()
	spawnTool := &spawnSubagentTool{mam: mam}

	requests := []struct {
		agent string
		skill string
		role  string
	}{
		{agent: "coder_agent", skill: "code_expert", role: "Implement production code."},
		{agent: "tester_agent", skill: "test_automation", role: "Design and run automated tests."},
		{agent: "doc_agent", skill: "documentation_specialist", role: "Write accurate documentation."},
		{agent: "security_agent", skill: "security_auditor", role: "Audit security boundaries."},
		{agent: "devops_agent", skill: "infrastructure_as_code", role: "Maintain deployment automation."},
	}

	for _, request := range requests {
		arguments, err := json.Marshal(map[string]any{
			"name":          request.agent,
			"system_prompt": request.role,
			"skill_names":   []string{request.skill},
		})
		if err != nil {
			t.Fatalf("marshal spawn request for %s: %v", request.agent, err)
		}
		if _, err := spawnTool.Execute(baseAgent, string(arguments)); err != nil {
			t.Fatalf("spawn %s: %v", request.agent, err)
		}

		spawned := mam.Agents[request.agent]
		if spawned == nil {
			t.Fatalf("%s was not registered", request.agent)
		}
		t.Cleanup(spawned.Cancel)
		if len(spawned.LocalSkills) != 1 || spawned.LocalSkills[0].Name != request.skill {
			t.Fatalf("%s has unexpected local skills: %+v", request.agent, spawned.LocalSkills)
		}
	}

	if got := mam.ListAgents(); len(got) != len(requests) {
		t.Fatalf("expected %d agents, got %d: %v", len(requests), len(got), got)
	}
	topology, err := (&swarmTopologyTool{mam: mam}).Execute(baseAgent, `{}`)
	if err != nil {
		t.Fatalf("read swarm topology: %v", err)
	}
	for _, request := range requests {
		if !strings.Contains(topology, request.agent) || !strings.Contains(topology, request.skill) {
			t.Fatalf("topology omits %s or %s:\n%s", request.agent, request.skill, topology)
		}
	}
}

func TestSpawnSubagentDefinitionListsAvailableReferenceSkills(t *testing.T) {
	baseAgent := &Agent{
		Config:   &config.Config{},
		Registry: tool.NewToolRegistry(),
		ActiveSkills: []tool.Skill{
			{Name: "test_automation"},
			{Name: "code_expert"},
		},
	}
	mam := NewMultiAgentManager(baseAgent, &bytes.Buffer{}, style.UITheme{})
	spawnTool := &spawnSubagentTool{mam: mam}

	definition := spawnTool.Definition()
	skillNames := definition.Function.Parameters.Properties["skill_names"]
	if skillNames.Items == nil {
		t.Fatal("skill_names schema has no item contract")
	}
	if got := strings.Join(skillNames.Items.Enum, ","); got != "code_expert,test_automation" {
		t.Fatalf("unexpected skill enum %q", got)
	}
	if _, exists := definition.Function.Parameters.Properties["inline_skills"]; !exists {
		t.Fatal("spawn_subagent schema does not expose inline_skills")
	}
}

func TestSpawnAgentRejectsUnsafeName(t *testing.T) {
	baseAgent := &Agent{
		Config:   &config.Config{},
		Registry: tool.NewToolRegistry(),
	}
	mam := NewMultiAgentManager(baseAgent, &bytes.Buffer{}, style.UITheme{})
	mam.agentsDir = t.TempDir()

	if err := mam.SpawnAgent("../escape", "unsafe", "", nil); err == nil {
		t.Fatal("expected path-like agent name to be rejected")
	}
}

func TestCompactPromptExplainsEmptySkillCatalog(t *testing.T) {
	baseAgent := &Agent{
		Config: &config.Config{
			CompactPrompt:     true,
			SystemInstruction: "base",
		},
		WorkspaceRoot: t.TempDir(),
	}

	prompt := baseAgent.GetSystemPrompt()
	if !strings.Contains(prompt, "No registered reference skills are currently installed") {
		t.Fatalf("compact prompt omits the empty skill catalog: %q", prompt)
	}
	if !strings.Contains(prompt, "inline_skills") {
		t.Fatalf("compact prompt omits inline skill guidance: %q", prompt)
	}
}
