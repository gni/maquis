package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/ui/style"
)

func TestMultiAgentSpawnSkills(t *testing.T) {
	cfg := &config.Config{}
	baseAgent := &Agent{
		Config: cfg,
		Registry: tool.NewToolRegistry(),
		ActiveSkills: []tool.Skill{
			{Name: "git_guru", Description: "Git expert", Content: "Use git"},
			{Name: "go_test_master", Description: "Go testing", Content: "Use testing"},
		},
	}

	var buf bytes.Buffer
	theme := style.UITheme{}
	mam := NewMultiAgentManager(baseAgent, &buf, theme)

	// Test case 1: Spawning generic agent (empty skillName)
	err := mam.SpawnAgent("generic_bob", "You are bob", "", "")
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
	err = mam.SpawnAgent("git_alice", "You are alice", "", "git_guru")
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
	err = mam.SpawnAgent("bad_agent", "You are bad", "", "non_existent_skill")
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
		Config: cfg,
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
	err := mam.SpawnAgent("parent_agent", "System Prompt Parent", "", "git_guru")
	if err != nil {
		t.Fatalf("failed to spawn parent: %v", err)
	}

	// Spawn subagent under parent
	err = mam.SpawnAgent("sub_agent", "System Prompt Sub", "parent_agent", "git_guru")
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
	err = mam2.KillAgent("sub_agent")
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

	// Spawn independent agent (no parent)
	err := mam.SpawnAgent("bob", "You are bob", "", "")
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
	err = mam.KillAgent("bob")
	if err != nil {
		t.Fatalf("failed to kill bob: %v", err)
	}

	_, ok = baseAgent.Registry.GetAllExecutors()["subagent__bob"]
	if ok {
		t.Errorf("expected tool 'subagent__bob' to be unregistered after agent was killed")
	}
}
