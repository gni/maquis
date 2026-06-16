package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterPlugins(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/workspace/maquis/tmp", "plugin_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pluginPath := filepath.Join(tmpDir, "mock_tool.sh")
	pluginContent := `#!/bin/bash
cat
`
	if err := os.WriteFile(pluginPath, []byte(pluginContent), 0755); err != nil {
		t.Fatalf("failed to write mock plugin: %v", err)
	}

	jsonPath := filepath.Join(tmpDir, "mock_tool.json")
	jsonContent := `{"name": "mock_tool", "description": "A mock tool for tests", "parameters": {"type": "object", "properties": {"msg": {"type": "string"}}, "required": ["msg"]}}`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write mock plugin json: %v", err)
	}

	if err := os.Chmod(pluginPath, 0755); err != nil {
		t.Fatalf("failed to make mock plugin executable: %v", err)
	}

	registry := NewToolRegistry()
	err = RegisterPlugins(registry, tmpDir)
	if err != nil {
		t.Fatalf("RegisterPlugins failed: %v", err)
	}

	expectedName := "plugin__mock_tool"
	executors := registry.GetAllExecutors()
	executor, exists := executors[expectedName]
	if !exists {
		t.Fatalf("expected tool '%s' to be registered", expectedName)
	}

	if executor.Name() != expectedName {
		t.Errorf("expected tool name '%s', got '%s'", expectedName, executor.Name())
	}

	def := executor.Definition()
	if def.Function.Description != "A mock tool for tests" {
		t.Errorf("expected description 'A mock tool for tests', got '%s'", def.Function.Description)
	}

	args := `{"msg": "test message"}`
	output, err := executor.Execute(nil, args)
	if err != nil {
		t.Fatalf("failed to execute registered plugin: %v", err)
	}

	if !strings.Contains(output, args) {
		t.Errorf("expected output to contain '%s', got '%s'", args, output)
	}
}

func TestRegisterPluginsInvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/workspace/maquis/tmp", "plugin_test_invalid_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pluginPath := filepath.Join(tmpDir, "invalid_tool.sh")
	pluginContent := `#!/bin/bash
exit 0
`
	if err := os.WriteFile(pluginPath, []byte(pluginContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}
	if err := os.Chmod(pluginPath, 0755); err != nil {
		t.Fatalf("failed to make script executable: %v", err)
	}

	jsonPath := filepath.Join(tmpDir, "invalid_tool.json")
	jsonContent := `this is not valid json`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("failed to write invalid plugin json: %v", err)
	}

	registry := NewToolRegistry()
	err = RegisterPlugins(registry, tmpDir)
	if err != nil {
		t.Fatalf("RegisterPlugins failed: %v", err)
	}

	executors := registry.GetAllExecutors()
	for name := range executors {
		if strings.Contains(name, "invalid_tool") {
			t.Errorf("unregistered tool '%s' was registered unexpectedly", name)
		}
	}
}
