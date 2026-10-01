package agent

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestKillAllTasksAndClearTasks(t *testing.T) {
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		NextTaskId:    1,
	}

	// Spawn a background task using SpawnTask
	var buf bytes.Buffer
	taskID, err := a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}

	task := a.Tasks[taskID]
	task.mu.Lock()
	status := task.Status
	task.mu.Unlock()
	if status != "running" {
		t.Fatalf("expected status running, got %s", status)
	}

	tasks := a.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in ListTasks, got %d", len(tasks))
	}

	// Test KillAllTasks
	killed := a.KillAllTasks()
	if killed != 1 {
		t.Fatalf("expected 1 task killed, got %d", killed)
	}

	task.mu.Lock()
	status = task.Status
	task.mu.Unlock()
	if status != "killed" {
		t.Fatalf("expected task status 'killed', got %s", status)
	}

	// Test ClearTasks
	// Add another task
	_, err = a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask 2 failed: %v", err)
	}

	cleared := a.ClearTasks()
	if cleared < 1 {
		t.Fatalf("expected >= 1 task cleared, got %d", cleared)
	}

	if len(a.ListTasks()) != 0 {
		t.Fatalf("expected 0 tasks after ClearTasks, got %d", len(a.ListTasks()))
	}
}

func TestBuildSystemPromptIncludesBackgroundTasks(t *testing.T) {
	cfg := SystemPromptConfig{
		ActiveTasks: []TaskInfo{
			{
				ID:       "task_7",
				Status:   "running",
				Duration: 5 * time.Minute,
				Command:  "uvicorn src.app:app --port 8000",
			},
		},
	}

	prompt := BuildSystemPrompt(cfg)
	if !strings.Contains(prompt, "<background_tasks>") {
		t.Fatalf("expected prompt to contain <background_tasks>, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "task_7") || !strings.Contains(prompt, "uvicorn") {
		t.Fatalf("expected prompt to describe task_7 and command, got:\n%s", prompt)
	}
}

func TestKillTaskNormalizedIdAndStatus(t *testing.T) {
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		NextTaskId:    1,
	}

	var buf bytes.Buffer
	taskID, err := a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}
	if taskID != "task_1" {
		t.Fatalf("expected taskID task_1, got %s", taskID)
	}

	// Should be able to query status using "1" instead of "task_1"
	status, _, err := a.GetTaskStatus("1")
	if err != nil {
		t.Fatalf("GetTaskStatus with normalized id '1' failed: %v", err)
	}
	if status != "running" {
		t.Fatalf("expected status running, got %s", status)
	}

	// Should be able to kill using "1" instead of "task_1"
	err = a.KillTask("1")
	if err != nil {
		t.Fatalf("KillTask with normalized id '1' failed: %v", err)
	}

	task := a.Tasks[taskID]
	task.mu.Lock()
	finalStatus := task.Status
	task.mu.Unlock()
	if finalStatus != "killed" {
		t.Fatalf("expected status 'killed', got %s", finalStatus)
	}

	// Wait for completion event
	select {
	case event := <-a.SystemEvents:
		if !strings.Contains(event, "task_1 finished: killed") {
			t.Fatalf("unexpected system event: %s", event)
		}
		if strings.Contains(event, "System Event:") || strings.Contains(event, "Please review") {
			t.Fatalf("system event contains old rogue prompt formatting: %s", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for task completion event")
	}
}

