package agent

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseManualCommand(t *testing.T) {
	tests := []struct {
		line          string
		enabled       bool
		expectedIsCmd bool
		expectedCmd   string
	}{
		// ! prefixed commands are always manual commands
		{"!git status", false, true, "git status"},
		{"!git status", true, true, "git status"},
		{"!ls -la", false, true, "ls -la"},

		// Direct commands enabled
		{"ls", true, true, "ls"},
		{"ls -la", true, true, "ls -la"},
		{"pwd", true, true, "pwd"},
		{"git status", true, true, "git status"},

		// Non-direct commands in new rule
		{"go build", true, false, ""},
		{"go run main.go", true, false, ""},
		{"mkdir src", true, false, ""},
		{"find .", true, false, ""},

		// Direct commands disabled
		{"ls", false, false, ""},
		{"pwd", false, false, ""},
		{"git status", false, false, ""},

		// Non-direct commands
		{"echo hello", true, false, ""},
		{"vim file.txt", true, false, ""},
	}

	for _, tt := range tests {
		isCmd, cmdStr := parseManualCommand(tt.line, tt.enabled)
		if isCmd != tt.expectedIsCmd {
			t.Errorf("parseManualCommand(%q, %v) returned isCmd = %v; want %v", tt.line, tt.enabled, isCmd, tt.expectedIsCmd)
		}
		if cmdStr != tt.expectedCmd {
			t.Errorf("parseManualCommand(%q, %v) returned cmd = %q; want %q", tt.line, tt.enabled, cmdStr, tt.expectedCmd)
		}
	}
}

func TestSafePath(t *testing.T) {
	a := &Agent{
		WorkspaceRoot: "/workspace/bidouille",
	}

	tests := []struct {
		inputPath string
		expectErr bool
		expected  string
	}{
		{"pkg/agent/loop.go", false, "/workspace/bidouille/pkg/agent/loop.go"},
		{"/workspace/bidouille/pkg/agent/loop.go", false, "/workspace/bidouille/pkg/agent/loop.go"},
		{"../loop.go", true, ""},
		{"/etc/passwd", true, ""},
		{"../../../etc/passwd", true, ""},
		{"", false, "/workspace/bidouille"},
		{".", false, "/workspace/bidouille"},
	}

	for _, tt := range tests {
		got, err := a.SafePath(tt.inputPath)
		if (err != nil) != tt.expectErr {
			t.Errorf("SafePath(%q) error status = %v; want error? %v", tt.inputPath, err, tt.expectErr)
			continue
		}
		if !tt.expectErr && got != tt.expected {
			t.Errorf("SafePath(%q) = %q; want %q", tt.inputPath, got, tt.expected)
		}
	}
}

func TestBackgroundTask(t *testing.T) {
	tempDirRoot := os.Getenv("GOTMPDIR")
	if tempDirRoot == "" {
		tempDirRoot = os.TempDir()
	}
	tempDir, err := os.MkdirTemp(tempDirRoot, "bidouille-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	a := &Agent{
		WorkspaceRoot: tempDir,
		Tasks:         make(map[string]*Task),
		NextTaskId:    1,
	}

	var buf bytes.Buffer
	id, err := a.SpawnTask("sleep 10", &buf)
	if err != nil {
		t.Fatalf("failed to spawn task: %v", err)
	}

	if id != "task_1" {
		t.Errorf("expected task ID 'task_1', got %q", id)
	}

	// Wait briefly for the task to be marked running/started
	time.Sleep(100 * time.Millisecond)

	status, _, err := a.GetTaskStatus(id)
	if err != nil {
		t.Fatalf("failed to get task status: %v", err)
	}
	if status != "running" {
		t.Errorf("expected status 'running', got %q", status)
	}

	list := a.ListTasks()
	if len(list) != 1 {
		t.Errorf("expected 1 task in list, got %d", len(list))
	} else {
		if list[0].ID != id {
			t.Errorf("expected task ID %q in list, got %q", id, list[0].ID)
		}
		if list[0].Status != "running" {
			t.Errorf("expected task status 'running' in list, got %q", list[0].Status)
		}
	}

	err = a.KillTask(id)
	if err != nil {
		t.Fatalf("failed to kill task: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	status, _, err = a.GetTaskStatus(id)
	if err != nil {
		t.Fatalf("failed to get task status after kill: %v", err)
	}
	if status != "killed" {
		t.Errorf("expected status 'killed', got %q", status)
	}

	id2, err := a.SpawnTask("echo 'hello bidouille'", &buf)
	if err != nil {
		t.Fatalf("failed to spawn second task: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	status2, output2, err := a.GetTaskStatus(id2)
	if err != nil {
		t.Fatalf("failed to get second task status: %v", err)
	}
	if status2 != "completed" {
		t.Errorf("expected second task status 'completed', got %q", status2)
	}
	if !strings.Contains(output2, "hello bidouille") {
		t.Errorf("expected output to contain 'hello bidouille', got %q", output2)
	}
}
