package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pathlibRe = regexp.MustCompile(`Path\(['"]([^'"]+)['"]\)`)
	catFileRe = regexp.MustCompile(`(?:cat|head|tail|wc)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	sedFileRe = regexp.MustCompile(`sed\s+-[a-zA-Z0-9]+\s+['"][^'"]+['"]\s+['"]?([^\s'"]+)`)
	lsDirRe   = regexp.MustCompile(`(?:ls|dir)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
)

// TurnExecutionGuard enforces limits on repetitive target inspections,
// re-reads of the same file, and repeated command failures during agent turns.
type TurnExecutionGuard struct {
	fileReadCounts            map[string]int
	lastCallKey               string
	consecutiveIdenticalCount int
	failedCommandCounts       map[string]int
	maxPerFileReads           int
	maxIdenticalCalls         int
}

// NewTurnExecutionGuard creates a new execution guard for an active turn.
func NewTurnExecutionGuard() *TurnExecutionGuard {
	return &TurnExecutionGuard{
		fileReadCounts:      make(map[string]int),
		failedCommandCounts: make(map[string]int),
		maxPerFileReads:     3,
		maxIdenticalCalls:   3,
	}
}

// ConsecutiveIdenticalCount returns the current consecutive identical tool call count.
func (g *TurnExecutionGuard) ConsecutiveIdenticalCount() int {
	if g == nil {
		return 0
	}
	return g.consecutiveIdenticalCount
}

// FileReadCount returns the number of times target has been inspected this turn.
func (g *TurnExecutionGuard) FileReadCount(target string) int {
	if g == nil {
		return 0
	}
	return g.fileReadCounts[filepath.Clean(target)]
}

// CheckPreExecution evaluates a proposed tool call BEFORE execution.
// If blocked, it returns a non-nil error indicating the constraint that was violated.
func (g *TurnExecutionGuard) CheckPreExecution(toolName, arguments string) error {
	if g == nil {
		return nil
	}

	trimmedArgs := strings.TrimSpace(arguments)
	callKey := toolName + ":" + trimmedArgs

	// 1. Repeated failing bash command (specific)
	if toolName == "bash" {
		cmd := extractBashCommand(arguments)
		if cmd != "" && g.failedCommandCounts[cmd] >= 2 {
			return fmt.Errorf("loop detected: command '%s' has failed repeatedly (%d times). Do not re-run this failing command; address the error or use 'edit'/'write'", cmd, g.failedCommandCounts[cmd])
		}
	}

	target := g.ExtractTargetFile(toolName, arguments)
	// 2. Per-target read/inspection limit across turn (prevents re-reading/re-listing the same file/dir)
	if target != "" {
		count := g.fileReadCounts[target]
		if count >= g.maxPerFileReads {
			return fmt.Errorf("loop detected: target '%s' has already been inspected %d times this turn and its full contents are already in context above. Do NOT re-read it. Call 'edit' or 'write' now to modify the code", target, count)
		}
	}

	// 3. Consecutive identical call limit (general)
	if callKey == g.lastCallKey && g.consecutiveIdenticalCount >= g.maxIdenticalCalls {
		return fmt.Errorf("loop detected: identical tool call repeated %d times. Stop repeating the same call and choose a different action", g.consecutiveIdenticalCount)
	}

	return nil
}

// RecordPostExecution updates tracking counters after a tool call completes.
func (g *TurnExecutionGuard) RecordPostExecution(toolName, arguments string, output string, err error) {
	if g == nil {
		return
	}

	trimmedArgs := strings.TrimSpace(arguments)
	callKey := toolName + ":" + trimmedArgs

	if callKey == g.lastCallKey {
		g.consecutiveIdenticalCount++
	} else {
		g.lastCallKey = callKey
		g.consecutiveIdenticalCount = 1
	}

	target := g.ExtractTargetFile(toolName, arguments)
	if target != "" {
		g.fileReadCounts[target]++
	}

	var cmd string
	if toolName == "bash" {
		cmd = extractBashCommand(arguments)
		if err != nil && cmd != "" {
			g.failedCommandCounts[cmd]++
		}
	}

	isModifying := toolName == "write" || toolName == "edit" || (toolName == "bash" && isBashModifying(cmd) && err == nil)
	if isModifying {
		g.fileReadCounts = make(map[string]int)
		g.failedCommandCounts = make(map[string]int)
		g.lastCallKey = ""
		g.consecutiveIdenticalCount = 0
		return
	}
}

// IsInspectionCall returns true if the tool call performs read-only inspection.
func (g *TurnExecutionGuard) IsInspectionCall(toolName, arguments string) bool {
	switch toolName {
	case "read", "list", "ls", "grep", "find", "load_skill", "task_status", "swarm_topology", "swarm_audit":
		return true
	case "bash":
		cmd := extractBashCommand(arguments)
		return isBashInspection(cmd)
	default:
		return false
	}
}

// ExtractTargetFile extracts the file or directory path being inspected by read, list, or bash commands.
func (g *TurnExecutionGuard) ExtractTargetFile(toolName, arguments string) string {
	if toolName == "read" {
		var args struct {
			Path     string `json:"path"`
			File     string `json:"file"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Path != "" {
				return filepath.Clean(args.Path)
			}
			if args.FilePath != "" {
				return filepath.Clean(args.FilePath)
			}
			if args.File != "" {
				return filepath.Clean(args.File)
			}
		}
		var raw string
		if err := json.Unmarshal([]byte(arguments), &raw); err == nil && raw != "" {
			return filepath.Clean(raw)
		}
		return ""
	}

	if toolName == "list" || toolName == "ls" {
		var args struct {
			Path    string `json:"path"`
			Dir     string `json:"dir"`
			DirPath string `json:"dir_path"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Path != "" {
				return filepath.Clean(args.Path)
			}
			if args.Dir != "" {
				return filepath.Clean(args.Dir)
			}
			if args.DirPath != "" {
				return filepath.Clean(args.DirPath)
			}
		}
		var raw string
		if err := json.Unmarshal([]byte(arguments), &raw); err == nil && raw != "" {
			return filepath.Clean(raw)
		}
		return "."
	}

	if toolName == "bash" {
		cmd := extractBashCommand(arguments)
		if m := pathlibRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := sedFileRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := catFileRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := lsDirRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
	}

	return ""
}

func extractBashCommand(arguments string) string {
	var args struct {
		Command   string `json:"command"`
		Cmd       string `json:"cmd"`
		Arguments string `json:"arguments"`
	}
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			return strings.TrimSpace(unquoted)
		}
	}
	if err := json.Unmarshal([]byte(arguments), &args); err == nil {
		if args.Command != "" {
			return strings.TrimSpace(args.Command)
		}
		if args.Cmd != "" {
			return strings.TrimSpace(args.Cmd)
		}
		if args.Arguments != "" {
			return strings.TrimSpace(args.Arguments)
		}
	}
	return trimmed
}

func isBashInspection(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	prefixes := []string{"cat ", "head ", "tail ", "sed -n", "awk ", "wc ", "more ", "less "}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "python") || strings.Contains(lower, "python3") {
		if (strings.Contains(lower, ".read_text()") || strings.Contains(lower, ".read()") || strings.Contains(lower, "open(")) &&
			!strings.Contains(lower, ".write_text(") && !strings.Contains(lower, ".write(") {
			return true
		}
	}
	return false
}

func isBashModifying(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	prefixes := []string{"git commit", "git add", "mkdir", "touch", "cp ", "mv ", "rm ", "npm ", "pnpm ", "yarn ", "pip ", "go get ", "cargo "}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	if strings.Contains(trimmed, "sed -i") || strings.Contains(trimmed, " > ") || strings.Contains(trimmed, " >> ") || strings.Contains(trimmed, "> ") {
		return true
	}
	return false
}
