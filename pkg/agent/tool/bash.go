package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type bashTool struct{}

func NewBashTool() ToolExecutor {
	return &bashTool{}
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "bash",
			Description: "Execute a shell command inside the workspace and return stdout and stderr.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"command": {
						Type:        "string",
						Description: "The command to run in the terminal.",
					},
					"background": {
						Type:        "boolean",
						Description: "Whether to run the command in the background (as a background task). Useful for long-running processes, servers, or large builds so you don't block.",
					},
				},
				Required: []string{"command"},
			},
		},
	}
}

func (t *bashTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Command    string `json:"command"`
		Background bool   `json:"background"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command parameter is empty")
	}

	if args.Background {
		id, err := ctx.SpawnTask(args.Command, os.Stderr)
		if err != nil {
			return "", fmt.Errorf("failed to spawn background task: %w", err)
		}
		return fmt.Sprintf("Task spawned in background with ID: %s. You can monitor its output using 'task_status' or kill it using 'task_kill'. Toggle live stream via Ctrl+O.", id), nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx.Context(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", args.Command)
	cmd.Dir = ctx.GetWorkspaceRoot()
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	// If the command timed out
	if timeoutCtx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("command timed out after 120 seconds. If this is a server or long-running process, use 'background: true'")
	}

	// SanitizeUTF8 helper is in file.go, which is in the same package (tool), so it can be called directly!
	output := SanitizeUTF8(stdout.Bytes())
	errOutput := SanitizeUTF8(stderr.Bytes())

	combined := output
	if errOutput != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += errOutput
	}

	lines := strings.Split(combined, "\n")
	if len(lines) > 200 {
		var truncatedLines []string
		truncatedLines = append(truncatedLines, lines[:20]...)
		truncatedLines = append(truncatedLines, fmt.Sprintf("\n... [ %d lines omitted to save context length ] ...\n", len(lines)-100))
		truncatedLines = append(truncatedLines, lines[len(lines)-80:]...)
		combined = strings.Join(truncatedLines, "\n")
	}

	if len(combined) > 50000 {
		combined = combined[:25000] + "\n... [ output too large, truncated middle ] ...\n" + combined[len(combined)-25000:]
	}

	if err != nil {
		if combined == "" {
			combined = fmt.Sprintf("command failed: %v", err)
		}
		return combined, fmt.Errorf("command failed: %w", err)
	}
	return combined, nil
}
