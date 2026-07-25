package tool

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

type bashTestContext struct {
	root string
}

func (c *bashTestContext) SafePath(inputPath string) (string, error) {
	return inputPath, nil
}

func (c *bashTestContext) GetWorkspaceRoot() string {
	return c.root
}

func (c *bashTestContext) GetActiveSkills() []Skill {
	return nil
}

func (c *bashTestContext) ReloadSkills() []Skill {
	return nil
}

func (c *bashTestContext) SpawnTask(string, io.Writer) (string, error) {
	return "", fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) GetTaskStatus(string) (string, string, error) {
	return "", "", fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) KillTask(string) error {
	return fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) Context() context.Context {
	return context.Background()
}

func (c *bashTestContext) HasSubagent(string) bool {
	return false
}

func TestBashFailureReturnsCapturedStderr(t *testing.T) {
	executor := NewBashTool()
	ctx := &bashTestContext{root: t.TempDir()}

	output, err := executor.Execute(
		ctx,
		`{"command":"printf 'npm ERR! unable to resolve dependency tree\\n' >&2; exit 17"}`,
	)
	if err == nil {
		t.Fatal("failing command returned no error")
	}
	if !strings.Contains(output, "npm ERR! unable to resolve dependency tree") {
		t.Fatalf("failing command lost stderr: %q", output)
	}
	if !strings.Contains(err.Error(), "exit status 17") {
		t.Fatalf("failing command lost its exit status: %v", err)
	}
}
