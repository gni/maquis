package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type grepTool struct{}

func NewGrepTool() ToolExecutor {
	return &grepTool{}
}

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

func (t *grepTool) Execute(ctx AgentContext, arguments string) (string, error) {
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

	safePath, err := ctx.SafePath(searchPath)
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
				relPath, err := filepath.Rel(ctx.GetWorkspaceRoot(), path)
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

func NewFindTool() ToolExecutor {
	return &findTool{}
}

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

func (t *findTool) Execute(ctx AgentContext, arguments string) (string, error) {
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

	safePath, err := ctx.SafePath(searchPath)
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
			relPath, err := filepath.Rel(ctx.GetWorkspaceRoot(), path)
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
