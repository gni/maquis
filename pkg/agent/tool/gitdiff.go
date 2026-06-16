package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type gitDiffTool struct{}

func NewGitDiffTool() ToolExecutor {
	return &gitDiffTool{}
}

func (t *gitDiffTool) Name() string { return "git_diff" }

func (t *gitDiffTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "git_diff",
			Description: "Get a summary of uncommitted git changes (status and diff stat) in the repository to check what code has been modified.",
			Parameters: JSONSchema{
				Type:       "object",
				Properties: map[string]SchemaProp{},
			},
		},
	}
}

func (t *gitDiffTool) Execute(ctx AgentContext, arguments string) (string, error) {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Run git status
	cmdStatus := exec.CommandContext(ctxTimeout, "git", "status", "--porcelain")
	var statusOut, statusErr bytes.Buffer
	cmdStatus.Stdout = &statusOut
	cmdStatus.Stderr = &statusErr
	err := cmdStatus.Run()
	if err != nil {
		return "", fmt.Errorf("git status failed: %w (details: %s)", err, strings.TrimSpace(statusErr.String()))
	}

	// 2. Run git diff --stat
	cmdDiff := exec.CommandContext(ctxTimeout, "git", "diff", "--stat")
	var diffOut, diffErr bytes.Buffer
	cmdDiff.Stdout = &diffOut
	cmdDiff.Stderr = &diffErr
	_ = cmdDiff.Run() // git diff returns non-zero status in some configurations, ignore code check

	statusText := strings.TrimSpace(statusOut.String())
	diffText := strings.TrimSpace(diffOut.String())

	var sb strings.Builder
	sb.WriteString("Git Status:\n")
	if statusText == "" {
		sb.WriteString("  Working tree clean (no uncommitted changes).\n")
	} else {
		for _, line := range strings.Split(statusText, "\n") {
			sb.WriteString(fmt.Sprintf("  %s\n", line))
		}
	}

	if diffText != "" {
		sb.WriteString("\nGit Diff Stat:\n")
		for _, line := range strings.Split(diffText, "\n") {
			sb.WriteString(fmt.Sprintf("  %s\n", line))
		}
	}

	return sb.String(), nil
}
