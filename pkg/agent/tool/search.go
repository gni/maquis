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

	var globRegex *regexp.Regexp
	if args.Glob != "" {
		var err error
		globRegex, err = globToRegexp(args.Glob)
		if err != nil {
			return "", fmt.Errorf("invalid glob pattern: %w", err)
		}
	}

	patterns := loadGitignorePatterns(ctx.GetWorkspaceRoot())
	err = filepath.Walk(safePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		isDir := info.IsDir()
		relPath, _ := filepath.Rel(safePath, path)
		if isIgnored(info.Name(), isDir) || (matchesGitignore(relPath, isDir, patterns) && (isDir || isCompiledOrLockfile(info.Name()))) || (strings.HasPrefix(info.Name(), ".") && info.Name() != ".") {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if isDir {
			return nil
		}

		if globRegex != nil {
			relToSearch, err := filepath.Rel(safePath, path)
			if err != nil {
				relToSearch = path
			}
			matchPath := filepath.ToSlash(relToSearch)
			if !globRegex.MatchString(matchPath) {
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
				matchedLine := strings.TrimSpace(line)
				if len(matchedLine) > 1000 {
					matchedLine = matchedLine[:1000] + " ... [line truncated: line is too long] ..."
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", relPath, i+1, matchedLine))
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
		limit = 500
	} else if limit > 1000 {
		limit = 1000
	}

	var regex *regexp.Regexp
	if args.Pattern != "" {
		var err error
		regex, err = globToRegexp(args.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}
	}

	var results []string
	patterns := loadGitignorePatterns(ctx.GetWorkspaceRoot())
	err = filepath.Walk(safePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		isDir := info.IsDir()
		relPath, _ := filepath.Rel(safePath, path)
		if isIgnored(info.Name(), isDir) || (matchesGitignore(relPath, isDir, patterns) && (isDir || isCompiledOrLockfile(info.Name()))) || (strings.HasPrefix(info.Name(), ".") && info.Name() != ".") {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		if regex != nil {
			relToSearch, err := filepath.Rel(safePath, path)
			if err != nil {
				relToSearch = path
			}
			matchPath := filepath.ToSlash(relToSearch)
			if regex.MatchString(matchPath) {
				relPath, err := filepath.Rel(ctx.GetWorkspaceRoot(), path)
				if err != nil {
					relPath = path
				}
				results = append(results, relPath)
				if len(results) >= limit {
					return fmt.Errorf("limit_reached")
				}
			}
		}
		return nil
	})

	limitReached := false
	if err != nil {
		if err.Error() == "limit_reached" {
			limitReached = true
		} else {
			return "", fmt.Errorf("find failed: %w", err)
		}
	}

	if len(results) == 0 {
		return "No matching files found.", nil
	}

	resStr := strings.Join(results, "\n")
	if limitReached {
		resStr += fmt.Sprintf("\n\n[Search truncated. Too many files matched the pattern (limit: %d). Use a more specific pattern.]", limit)
	}
	return resStr, nil
}

func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	
	if !strings.Contains(pattern, "/") {
		sb.WriteString("(.*/)?")
	}

	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					sb.WriteString("(.*/)?")
					i += 2
				} else {
					sb.WriteString(".*")
					i++
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '.':
			sb.WriteString("\\.")
		case '\\', '+', '^', '$', '(', ')', '|':
			sb.WriteString("\\" + string(c))
		default:
			sb.WriteString(string(c))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}
