package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
			Description: "Execute a shell command inside the workspace (such as builds, tests, package installation, git commands, and process management). Use dedicated tools for file operations: use 'read' instead of cat/head/tail, and 'edit'/'write' instead of sed/echo",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"command": {
						Type:        "string",
						Description: "The command to run in the terminal",
					},
					"background": {
						Type:        "boolean",
						Description: "Whether to run the command in the background as a background task",
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
		Cmd        string `json:"cmd"`
		Arguments  string `json:"arguments"`
		Background bool   `json:"background"`
	}
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			args.Command = unquoted
		}
	} else if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Command == "" {
		if args.Cmd != "" {
			args.Command = args.Cmd
		} else if args.Arguments != "" {
			args.Command = args.Arguments
		}
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
		exitCode := -1
		var exitDesc string
		if timeoutCtx.Err() == context.DeadlineExceeded {
			exitDesc = "command timed out after 120 seconds. If this is a server or long-running process, use 'background: true'"
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				sig := ws.Signal()
				switch sig {
				case syscall.SIGKILL:
					exitDesc = fmt.Sprintf("command terminated by SIGKILL (signal 9 / killed, possibly out of memory, exit status %d)", exitCode)
				case syscall.SIGSEGV:
					exitDesc = fmt.Sprintf("command terminated by SIGSEGV (signal 11 / segmentation fault, exit status %d)", exitCode)
				case syscall.SIGTERM:
					exitDesc = fmt.Sprintf("command terminated by SIGTERM (signal 15 / termination request, exit status %d)", exitCode)
				case syscall.SIGINT:
					exitDesc = fmt.Sprintf("command interrupted by SIGINT (signal 2 / Ctrl+C, exit status %d)", exitCode)
				case syscall.SIGABRT:
					exitDesc = fmt.Sprintf("command aborted by SIGABRT (signal 6 / abort, exit status %d)", exitCode)
				default:
					exitDesc = fmt.Sprintf("command terminated by signal %d (%s, exit status %d)", sig, sig.String(), exitCode)
				}
			} else {
				switch exitCode {
				case 127:
					exitDesc = "command not found: exit status 127"
				case 126:
					exitDesc = "command cannot execute: exit status 126 (permission denied)"
				default:
					exitDesc = fmt.Sprintf("command failed: exit status %d", exitCode)
				}
			}
		} else {
			exitDesc = fmt.Sprintf("command failed: %v", err)
		}

		if strings.TrimSpace(combined) == "" {
			combined = fmt.Sprintf("%s (no output on stdout or stderr)", exitDesc)
		} else if !strings.Contains(combined, "exit status") && !strings.Contains(combined, "exit code") {
			combined = fmt.Sprintf("%s\n\n(%s)", strings.TrimRight(combined, "\r\n"), exitDesc)
		}
		return combined, fmt.Errorf("%s", exitDesc)
	}
	return combined, nil
}
