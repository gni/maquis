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

func (t *bashTool) PromptSnippet() string {
	return "Execute shell commands (builds, tests, git, background processes)"
}

func (t *bashTool) PromptGuidelines() []string {
	return []string{
		"Use dedicated tools for file operations (read, edit, write, grep, find) instead of shell commands.",
		"For background processes and long-running services, set 'background': true.",
	}
}

func cleanBackgroundCommand(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	if idx := strings.Index(trimmed, "& echo"); idx != -1 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	trimmed = strings.TrimSuffix(trimmed, "&")
	trimmed = strings.TrimSpace(trimmed)
	if strings.HasPrefix(trimmed, "nohup ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "nohup "))
	}
	trimmed = strings.ReplaceAll(trimmed, "&& nohup ", "&& ")
	trimmed = strings.ReplaceAll(trimmed, "; nohup ", "; ")
	return trimmed
}

func (t *bashTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "bash",
			Description: "Execute shell commands inside the workspace (builds, tests, package installation, git commands, and process management).",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"command": {
						Type:        "string",
						Description: "The command to run in the terminal",
					},
					"background": {
						Type:        "boolean",
						Description: "Set to true to run the command in the background as a tracked background task (for servers, daemons, or long-running tasks).",
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

	isBgCmd := args.Background
	trimmedCmd := strings.TrimSpace(args.Command)
	if !isBgCmd {
		if strings.HasPrefix(trimmedCmd, "nohup ") ||
			strings.HasSuffix(trimmedCmd, "&") ||
			strings.Contains(trimmedCmd, " & ") ||
			strings.Contains(trimmedCmd, "& echo") {
			isBgCmd = true
			args.Command = cleanBackgroundCommand(args.Command)
		}
	}

	if isBgCmd {
		id, err := ctx.SpawnTask(args.Command, os.Stderr)
		if err != nil {
			return "", fmt.Errorf("failed to spawn background task: %w", err)
		}
		return fmt.Sprintf("Task spawned in background with ID: %s. You can monitor its output using 'task_status' or kill it using 'task_kill'. Toggle live stream via Ctrl+O.", id), nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx.Context(), 120*time.Second)
	defer cancel()

	cmd := exec.Command("bash", "-c", args.Command)
	cmd.Dir = ctx.GetWorkspaceRoot()
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start command: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case <-timeoutCtx.Done():
		if cmd.Process != nil {
			pgid, pgErr := syscall.Getpgid(cmd.Process.Pid)
			if pgErr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}
		}
		<-done
		if timeoutCtx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("command timed out after 120 seconds. If this is a server or long-running process, use 'background: true'")
		} else {
			err = fmt.Errorf("command cancelled by user")
		}
	case err = <-done:
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
