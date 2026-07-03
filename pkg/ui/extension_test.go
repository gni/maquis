package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
)

func TestRunExtension(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/workspace/maquis/tmp", "extension_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	extDir := filepath.Join(tmpDir, "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatalf("failed to create extensions dir: %v", err)
	}

	scriptPath := filepath.Join(extDir, "hello.sh")
	scriptContent := `#!/bin/bash
echo "Args: $@"
echo -n "Stdin: "
cat
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := &agent.Agent{
		Config:        &config.Config{},
		WorkspaceRoot: tmpDir,
	}

	messages := []db.Message{
		{Role: "user", Content: "Hello Agent"},
		{Role: "assistant", Content: "Hello User"},
	}

	var buf bytes.Buffer
	handled, err := RunExtension(a, "/hello", []string{"foo", "bar"}, &messages, &buf)
	if err != nil {
		t.Fatalf("RunExtension failed: %v", err)
	}
	if !handled {
		t.Errorf("expected extension to be handled, got false")
	}

	output := buf.String()
	if !strings.Contains(output, "Args: foo bar") {
		t.Errorf("expected output to contain forwarded arguments 'foo bar', got:\n%s", output)
	}

	if !strings.Contains(output, "Stdin: ") {
		t.Errorf("expected output to contain 'Stdin: ', got:\n%s", output)
	}

	if !strings.Contains(output, "Hello Agent") || !strings.Contains(output, "Hello User") {
		t.Errorf("expected output to contain json serialized messages, got:\n%s", output)
	}
}

func TestRunExtensionValidation(t *testing.T) {
	a := &agent.Agent{
		Config:        &config.Config{},
		WorkspaceRoot: "/workspace/maquis",
	}
	var buf bytes.Buffer

	handled, err := RunExtension(a, "/../evil", nil, nil, &buf)
	if err != nil {
		t.Errorf("unexpected error on invalid name: %v", err)
	}
	if handled {
		t.Errorf("expected handled=false for traversal attempt, got true")
	}

	handled, err = RunExtension(a, "/hello$world", nil, nil, &buf)
	if err != nil {
		t.Errorf("unexpected error on invalid character: %v", err)
	}
	if handled {
		t.Errorf("expected handled=false for invalid characters, got true")
	}
}
