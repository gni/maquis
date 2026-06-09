package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

func GetAvailableTools(allowlist []string) []Tool {
	allTools := []Tool{
		{
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
					},
					Required: []string{"command"},
				},
			},
		},
		{
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
		},
		{
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
		},
		{
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
		},
		{
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
		},
		{
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
		},
		{
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
		},
	}

	// Append load_skill tool
	allTools = append(allTools, Tool{
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
	})

	// Add MCP tools dynamically
	allTools = append(allTools, GetMCPTools()...)

	if len(allowlist) == 0 {
		return allTools
	}

	var filtered []Tool
	for _, t := range allTools {
		for _, allowed := range allowlist {
			if t.Function.Name == allowed || strings.HasPrefix(t.Function.Name, "mcp__") {
				filtered = append(filtered, t)
				break
			}
		}
	}
	return filtered
}

func ExecuteTool(name string, arguments string) (string, error) {
	switch name {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Command == "" {
			return "", fmt.Errorf("command parameter is empty")
		}

		cmd := exec.Command("bash", "-c", args.Command)
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

	case "read":
		var args struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}

		info, err := os.Stat(args.Path)
		if err != nil {
			return "", fmt.Errorf("failed to read file info: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("cannot read: path '%s' is a directory", args.Path)
		}
		if info.Size() > 2*1024*1024 { // 2MB limit
			return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is 2MB", info.Size())
		}

		data, err := os.ReadFile(args.Path)
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

	case "write":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"write_content"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}

		dir := filepath.Dir(args.Path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}

		err := os.WriteFile(args.Path, []byte(args.Content), 0644)
		if err != nil {
			return "", fmt.Errorf("failed to write file: %w", err)
		}
		return fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path), nil

	case "edit":
		var args struct {
			Path    string        `json:"path"`
			Edits   []ReplaceEdit `json:"updates"`
			OldText string        `json:"oldText,omitempty"`
			NewText string        `json:"newText,omitempty"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}

		edits := args.Edits
		if args.OldText != "" && args.NewText != "" {
			edits = append(edits, ReplaceEdit{OldText: args.OldText, NewText: args.NewText})
		}

		if len(edits) == 0 {
			return "", fmt.Errorf("no edits specified to apply")
		}

		data, err := os.ReadFile(args.Path)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		content := string(data)

		for i, edit := range edits {
			occurrences := strings.Count(content, edit.OldText)
			if occurrences == 0 {
				return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s", i, args.Path)
			}
			if occurrences > 1 {
				return "", fmt.Errorf("edit[%d]: oldText block is not unique; found %d occurrences in %s", i, occurrences, args.Path)
			}

			content = strings.Replace(content, edit.OldText, edit.NewText, 1)
		}

		err = os.WriteFile(args.Path, []byte(content), 0644)
		if err != nil {
			return "", fmt.Errorf("failed to write modified content back: %w", err)
		}

		return fmt.Sprintf("Successfully edited %s by applying %d replacement block(s).", args.Path, len(edits)), nil

	case "grep":
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

		err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
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
					results = append(results, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
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

	case "find":
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

		limit := args.Limit
		if limit <= 0 {
			limit = 1000
		}

		var results []string
		err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
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
				results = append(results, path)
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

	case "ls":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		}

		searchPath := args.Path
		if searchPath == "" {
			searchPath = "."
		}

		entries, err := os.ReadDir(searchPath)
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

	case "load_skill":
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		for _, s := range ActiveSkills {
			if s.Name == args.Name {
				return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
			}
		}
		return "", fmt.Errorf("skill '%s' not found", args.Name)

	default:
		if strings.HasPrefix(name, "mcp__") {
			return ExecuteMCPTool(name, arguments)
		}
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

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
