package tool

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type findTool struct{}

// NewFindTool creates a new file finder tool.
func NewFindTool() ToolExecutor {
	return &findTool{}
}

func (t *findTool) Name() string { return "find" }

func (t *findTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "find",
			Description: "Find files by glob pattern",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"pattern": {
						Type:        "string",
						Description: "Glob pattern to match files (e.g. *.py, **/*.json)",
					},
					"path": {
						Type:        "string",
						Description: "Directory to search in (default: current directory)",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum results to return (default: 500)",
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
		Glob    string `json:"glob"`
		Path    string `json:"path"`
		Dir     string `json:"dir"`
		Limit   int    `json:"limit"`
	}

	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			args.Pattern = unquoted
		}
	} else if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
			args.Pattern = trimmed
		} else {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}

	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		pattern = strings.TrimSpace(args.Glob)
	}
	if pattern == "" {
		return "", fmt.Errorf("pattern is required for find (e.g. '*.py' or '**/*.json')")
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = args.Dir
	}
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 500
	} else if limit > 1000 {
		limit = 1000
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := loadGitIgnore(workspaceRoot)

	lowerPattern := strings.ToLower(pattern)
	hasSlash := strings.Contains(pattern, "/")

	var matchedPaths []string
	limitReached := false

	if !info.IsDir() {
		rel, err := filepath.Rel(workspaceRoot, safePath)
		if err != nil {
			rel = safePath
		}
		rel = filepath.ToSlash(rel)
		matchedPaths = append(matchedPaths, rel)
	} else {
		walkErr := filepath.WalkDir(safePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			name := d.Name()
			if d.IsDir() {
				if isIgnoredDirName(name) || hasIgnoredComponent(path) || isIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
					return filepath.SkipDir
				}
				return nil
			}

			if isIgnoredFileName(name) || isIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
				return nil
			}

			rel, err := filepath.Rel(safePath, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)

			matched := false
			if hasSlash {
				matched, _ = filepath.Match(lowerPattern, strings.ToLower(rel))
			} else {
				matched, _ = filepath.Match(lowerPattern, strings.ToLower(name))
			}

			if matched {
				fullRel, err := filepath.Rel(workspaceRoot, path)
				if err != nil {
					fullRel = path
				}
				matchedPaths = append(matchedPaths, filepath.ToSlash(fullRel))
				if len(matchedPaths) >= limit {
					limitReached = true
					return fs.SkipAll
				}
			}
			return nil
		})

		if walkErr != nil && walkErr != fs.SkipAll {
			return "", walkErr
		}
	}

	if len(matchedPaths) == 0 {
		return fmt.Sprintf("No files found matching pattern: %s", pattern), nil
	}

	sort.Strings(matchedPaths)
	result := strings.Join(matchedPaths, "\n")
	if limitReached {
		result += fmt.Sprintf("\n\n[match limit reached: showing first %d matches. Narrow your search path or pattern.]", limit)
	}

	return result, nil
}
