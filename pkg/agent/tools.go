package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type ToolExecutor interface {
	Name() string
	Definition() Tool
	Execute(a *Agent, arguments string) (string, error)
}

type ToolRegistry struct {
	tools map[string]ToolExecutor
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]ToolExecutor)}
}

func (r *ToolRegistry) Register(t ToolExecutor) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) UnregisterPrefix(prefix string) {
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
		}
	}
}

func (r *ToolRegistry) Execute(a *Agent, name string, arguments string) (string, error) {
	executor, exists := r.tools[name]
	if !exists {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return executor.Execute(a, arguments)
}

func (r *ToolRegistry) GetAvailableTools(allowlist []string) []Tool {
	var allTools []Tool
	for name, t := range r.tools {
		if len(allowlist) == 0 {
			allTools = append(allTools, t.Definition())
		} else {
			allowed := false
			for _, allowedName := range allowlist {
				if name == allowedName || strings.HasPrefix(name, "mcp__") {
					allowed = true
					break
				}
			}
			if allowed {
				allTools = append(allTools, t.Definition())
			}
		}
	}

	// Sort deterministically
	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Function.Name < allTools[j].Function.Name
	})

	return allTools
}

// ----------------------------------------------------
// Built-in Tool Executors
// ----------------------------------------------------

type bashTool struct{}

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

func (t *bashTool) Execute(a *Agent, arguments string) (string, error) {
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
		id, err := a.SpawnTask(args.Command, os.Stderr)
		if err != nil {
			return "", fmt.Errorf("failed to spawn background task: %w", err)
		}
		return fmt.Sprintf("Task spawned in background with ID: %s. You can monitor its output using 'task_status' or kill it using 'task_kill'. Toggle live stream via Ctrl+O.", id), nil
	}

	cmd := exec.Command("bash", "-c", args.Command)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	output := sanitizeUTF8(stdout.Bytes())
	errOutput := sanitizeUTF8(stderr.Bytes())

	combined := ""
	if output != "" {
		combined += fmt.Sprintf("STDOUT:\n%s\n", output)
	}
	if errOutput != "" {
		combined += fmt.Sprintf("STDERR:\n%s\n", errOutput)
	}
	if combined == "" {
		combined = "(command completed with no output)"
	}

	if err != nil {
		return combined, fmt.Errorf("command failed: %w", err)
	}
	return combined, nil
}

type readTool struct{}

func (t *readTool) Name() string { return "read" }

func (t *readTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "read",
			Description: "Read the contents of a file with optional line offsets and limits.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the file to read (relative or absolute).",
					},
					"offset": {
						Type:        "number",
						Description: "Line number to start reading from (1-indexed, optional).",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of lines to read (optional).",
					},
				},
				Required: []string{"path"},
			},
		},
	}
}

func (t *readTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	safePath, err := a.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file info: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("cannot read: path '%s' is a directory", args.Path)
	}
	if info.Size() > 2*1024*1024 { // 2MB limit
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is 2MB", info.Size())
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Check if file is binary
	if isBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files")
	}

	contentStr := sanitizeUTF8(data)
	if strings.TrimSpace(contentStr) == "" {
		return "", fmt.Errorf("file is empty or contains only whitespace")
	}

	lines := strings.Split(contentStr, "\n")
	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}
	end := len(lines)
	if args.Limit > 0 {
		end = offset + args.Limit - 1
		if end > len(lines) {
			end = len(lines)
		}
	}
	return strings.Join(lines[offset-1:end], "\n"), nil
}

type writeTool struct{}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "write",
			Description: "Create a new file or overwrite an existing file with the specified content.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the target file.",
					},
					"write_content": {
						Type:        "string",
						Description: "Complete content to write into the file.",
					},
				},
				Required: []string{"path", "write_content"},
			},
		},
	}
}

func (t *writeTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"write_content"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	safePath, err := a.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	err = os.WriteFile(safePath, []byte(args.Content), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path), nil
}

type editTool struct{}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "edit",
			Description: "Edit a single file using exact search and replace text blocks. oldText must match exactly.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the file to edit.",
					},
					"updates": {
						Type:        "array",
						Description: "List of replacement blocks. Each contains 'oldText' and 'newText'.",
					},
				},
				Required: []string{"path", "updates"},
			},
		},
	}
}

func (t *editTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Path    string        `json:"path"`
		Edits   []ReplaceEdit `json:"updates"`
		OldText string        `json:"oldText,omitempty"`
		NewText string        `json:"newText,omitempty"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	safePath, err := a.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	edits := args.Edits
	if args.OldText != "" && args.NewText != "" {
		edits = append(edits, ReplaceEdit{OldText: args.OldText, NewText: args.NewText})
	}

	if len(edits) == 0 {
		return "", fmt.Errorf("no edits specified to apply")
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := string(data)

	var diffBuilder strings.Builder
	for i, edit := range edits {
		indexOfOldText := strings.Index(content, edit.OldText)
		if indexOfOldText == -1 {
			return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s", i, args.Path)
		}
		occurrences := strings.Count(content, edit.OldText)
		if occurrences > 1 {
			return "", fmt.Errorf("edit[%d]: oldText block is not unique; found %d occurrences in %s", i, occurrences, args.Path)
		}

		startLine := strings.Count(content[:indexOfOldText], "\n") + 1
		oldLines := strings.Split(edit.OldText, "\n")
		newLines := strings.Split(edit.NewText, "\n")
		numOldLines := len(oldLines)

		allLines := strings.Split(content, "\n")
		content = strings.Replace(content, edit.OldText, edit.NewText, 1)

		// Format header
		diffBuilder.WriteString(fmt.Sprintf("\x1b[1medit:%s\x1b[0m\n", args.Path))

		// Context before (3 lines)
		contextStart := startLine - 3
		if contextStart < 1 {
			contextStart = 1
		}
		for lineNum := contextStart; lineNum < startLine; lineNum++ {
			if lineNum <= len(allLines) {
				diffBuilder.WriteString(fmt.Sprintf("%-4d   %s\n", lineNum, allLines[lineNum-1]))
			}
		}

		// Deleted lines (red)
		for j, oldLine := range oldLines {
			lineNum := startLine + j
			diffBuilder.WriteString(fmt.Sprintf("\x1b[31m%-4d - %s\x1b[0m\n", lineNum, oldLine))
		}

		// Added lines (green)
		for j, newLine := range newLines {
			lineNum := startLine + j
			diffBuilder.WriteString(fmt.Sprintf("\x1b[32m%-4d + %s\x1b[0m\n", lineNum, newLine))
		}

		// Context after (3 lines)
		contextEnd := startLine + numOldLines + 2
		if contextEnd > len(allLines) {
			contextEnd = len(allLines)
		}
		for lineNum := startLine + numOldLines; lineNum <= contextEnd; lineNum++ {
			diffBuilder.WriteString(fmt.Sprintf("%-4d   %s\n", lineNum, allLines[lineNum-1]))
		}
	}

	err = os.WriteFile(safePath, []byte(content), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write modified content back: %w", err)
	}

	return diffBuilder.String(), nil
}

type grepTool struct{}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "grep",
			Description: "Recursively search for lines containing the pattern in a directory.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"pattern": {
						Type:        "string",
						Description: "Search pattern (regex or literal string).",
					},
					"path": {
						Type:        "string",
						Description: "Directory or file to search (default: current directory).",
					},
					"glob": {
						Type:        "string",
						Description: "Filter files by glob pattern, e.g. '*.ts' or '**/*.json'.",
					},
					"ignoreCase": {
						Type:        "boolean",
						Description: "Case-insensitive search (default: false).",
					},
					"literal": {
						Type:        "boolean",
						Description: "Treat pattern as literal string instead of regex (default: false).",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of matches to return (default: 100).",
					},
				},
				Required: []string{"pattern"},
			},
		},
	}
}

func (t *grepTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignoreCase"`
		Literal    bool   `json:"literal"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := a.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}

	var results []string
	var regex *regexp.Regexp

	if !args.Literal {
		pattern := args.Pattern
		if args.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		var err error
		regex, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	err = filepath.Walk(safePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		if args.Glob != "" {
			matched, err := filepath.Match(args.Glob, info.Name())
			if err != nil || !matched {
				return nil
			}
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			matched := false
			if args.Literal {
				if args.IgnoreCase {
					matched = strings.Contains(strings.ToLower(line), strings.ToLower(args.Pattern))
				} else {
					matched = strings.Contains(line, args.Pattern)
				}
			} else {
				matched = regex.MatchString(line)
			}

			if matched {
				relPath, err := filepath.Rel(a.WorkspaceRoot, path)
				if err != nil {
					relPath = path
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", relPath, i+1, strings.TrimSpace(line)))
				if len(results) >= limit {
					return fmt.Errorf("limit_reached")
				}
			}
		}
		return nil
	})

	if err != nil && err.Error() != "limit_reached" {
		return "", fmt.Errorf("grep failed: %w", err)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	return strings.Join(results, "\n"), nil
}

type findTool struct{}

func (t *findTool) Name() string { return "find" }

func (t *findTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "find",
			Description: "Find files in a directory that match a specific glob pattern.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"pattern": {
						Type:        "string",
						Description: "Glob pattern to match files, e.g. '*.ts' or 'src/**/*.go'.",
					},
					"path": {
						Type:        "string",
						Description: "Directory to search in (default: current directory).",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of results (default: 1000).",
					},
				},
				Required: []string{"pattern"},
			},
		},
	}
}

func (t *findTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := a.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 1000
	}

	var results []string
	err = filepath.Walk(safePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		matched, err := filepath.Match(args.Pattern, info.Name())
		if err == nil && matched {
			relPath, err := filepath.Rel(a.WorkspaceRoot, path)
			if err != nil {
				relPath = path
			}
			results = append(results, relPath)
			if len(results) >= limit {
				return fmt.Errorf("limit_reached")
			}
		}
		return nil
	})

	if err != nil && err.Error() != "limit_reached" {
		return "", fmt.Errorf("find failed: %w", err)
	}

	if len(results) == 0 {
		return "No matching files found.", nil
	}

	return strings.Join(results, "\n"), nil
}

type lsTool struct{}

func (t *lsTool) Name() string { return "ls" }

func (t *lsTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "ls",
			Description: "List files and directories in a path.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path of the directory to list.",
					},
				},
				Required: []string{},
			},
		},
	}
}

func (t *lsTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := a.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Contents of %s:\n", searchPath))
	for _, entry := range entries {
		info, err := entry.Info()
		typeStr := "file"
		size := int64(0)
		if entry.IsDir() {
			typeStr = "dir "
		} else if err == nil {
			size = info.Size()
		}

		sb.WriteString(fmt.Sprintf("  [%s]  %-25s  %d bytes\n", typeStr, entry.Name(), size))
	}
	return sb.String(), nil
}

type loadSkillTool struct{}

func (t *loadSkillTool) Name() string { return "load_skill" }

func (t *loadSkillTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "load_skill",
			Description: "Retrieve the detailed instructions, tools, or references for a specific skill from the available skills list.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"name": {
						Type:        "string",
						Description: "The name of the skill to load (e.g. 'agent-isolation').",
					},
				},
				Required: []string{"name"},
			},
		},
	}
}

func (t *loadSkillTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	for _, s := range a.ActiveSkills {
		if s.Name == args.Name {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}
	return "", fmt.Errorf("skill '%s' not found", args.Name)
}

// ----------------------------------------------------
// Helpers
// ----------------------------------------------------

func isBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func sanitizeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}

	var r []rune
	for len(data) > 0 {
		run, size := utf8.DecodeRune(data)
		if run == utf8.RuneError && size == 1 {
			r = append(r, ' ')
		} else {
			r = append(r, run)
		}
		data = data[size:]
	}
	return string(r)
}

// ----------------------------------------------------
// Task Management Tools
// ----------------------------------------------------

type taskStatusTool struct{}

func (t *taskStatusTool) Name() string { return "task_status" }

func (t *taskStatusTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "task_status",
			Description: "Retrieve the execution status and buffered stdout/stderr output of a background task.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"task_id": {
						Type:        "string",
						Description: "The ID of the background task (e.g. 'task_1').",
					},
				},
				Required: []string{"task_id"},
			},
		},
	}
}

func (t *taskStatusTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		TaskId string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	status, output, err := a.GetTaskStatus(args.TaskId)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s is currently: %s\n\nOutput:\n%s", args.TaskId, status, output), nil
}

type taskKillTool struct{}

func (t *taskKillTool) Name() string { return "task_kill" }

func (t *taskKillTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "task_kill",
			Description: "Terminate a running background task.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"task_id": {
						Type:        "string",
						Description: "The ID of the task to terminate.",
					},
				},
				Required: []string{"task_id"},
			},
		},
	}
}

func (t *taskKillTool) Execute(a *Agent, arguments string) (string, error) {
	var args struct {
		TaskId string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	err := a.KillTask(args.TaskId)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s successfully terminated.", args.TaskId), nil
}
