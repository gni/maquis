package tool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultGrepLimit     = 100
	maxGrepLimit         = 500
	maxGrepLineLength    = 500
	maxGrepOutputBytes   = 64 * 1024
	maxGrepFileSize      = 2 * 1024 * 1024 // 2MB
)

type grepTool struct{}

// NewGrepTool initializes a new recursive content search tool.
func NewGrepTool() ToolExecutor {
	return &grepTool{}
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "grep",
			Description: "Search file contents for regular expressions or literal strings across the workspace (like grep -rn). Returns matching file paths, line numbers, and lines. Automatically respects .gitignore and ignores dependency/build directories.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"pattern": {
						Type:        "string",
						Description: "The regular expression or literal string to search for.",
					},
					"path": {
						Type:        "string",
						Description: "Directory or file path to search within (relative to workspace, defaults to '.').",
					},
					"glob": {
						Type:        "string",
						Description: "Optional glob pattern to filter filenames (e.g. '*.go', '*.py', 'src/**/*.ts').",
					},
					"ignore_case": {
						Type:        "boolean",
						Description: "Case-insensitive search (default: false).",
					},
					"literal": {
						Type:        "boolean",
						Description: "Treat pattern as a literal string instead of a regular expression (default: false).",
					},
					"context": {
						Type:        "number",
						Description: "Number of context lines to display before and after each match (default: 0).",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of matching lines to return (default: 100, max: 500).",
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
		Query      string `json:"query"`
		Q          string `json:"q"`
		Path       string `json:"path"`
		SearchPath string `json:"search_path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		IgnoreCaseCamel bool `json:"ignoreCase"`
		Literal    bool   `json:"literal"`
		Context    int    `json:"context"`
		Limit      int    `json:"limit"`
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

	pattern := args.Pattern
	if pattern == "" {
		if args.Query != "" {
			pattern = args.Query
		} else if args.Q != "" {
			pattern = args.Q
		}
	}
	if pattern == "" {
		return "", fmt.Errorf("pattern is required for grep")
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = args.SearchPath
	}
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	ignoreCase := args.IgnoreCase || args.IgnoreCaseCamel
	literal := args.Literal

	limit := args.Limit
	if limit <= 0 {
		limit = defaultGrepLimit
	} else if limit > maxGrepLimit {
		limit = maxGrepLimit
	}

	contextLines := args.Context
	if contextLines < 0 {
		contextLines = 0
	} else if contextLines > 5 {
		contextLines = 5
	}

	var matchFunc func(line string) bool
	if literal {
		if ignoreCase {
			lowerPattern := strings.ToLower(pattern)
			matchFunc = func(line string) bool {
				return strings.Contains(strings.ToLower(line), lowerPattern)
			}
		} else {
			matchFunc = func(line string) bool {
				return strings.Contains(line, pattern)
			}
		}
	} else {
		regexPattern := pattern
		if ignoreCase && !strings.HasPrefix(regexPattern, "(?i)") {
			regexPattern = "(?i)" + regexPattern
		}
		re, err := regexp.Compile(regexPattern)
		if err != nil {
			return "", fmt.Errorf("invalid regular expression '%s': %w", pattern, err)
		}
		matchFunc = func(line string) bool {
			return re.MatchString(line)
		}
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := loadGitIgnore(workspaceRoot)

	globPattern := strings.TrimSpace(args.Glob)
	if globPattern != "" {
		globPattern = strings.ToLower(globPattern)
	}

	var out strings.Builder
	totalMatches := 0
	limitReached := false
	outputSizeReached := false

	processFile := func(filePath string) error {
		if selectErr := ctx.Context().Err(); selectErr != nil {
			return selectErr
		}

		fInfo, err := os.Stat(filePath)
		if err != nil || fInfo.IsDir() || fInfo.Size() == 0 || fInfo.Size() > maxGrepFileSize {
			return nil
		}

		data, err := os.ReadFile(filePath)
		if err != nil || isBinary(data) {
			return nil
		}

		contentStr := SanitizeUTF8(data)
		fileLines := strings.Split(contentStr, "\n")
		numLines := len(fileLines)

		relPath, err := filepath.Rel(workspaceRoot, filePath)
		if err != nil {
			relPath = filePath
		}
		relPath = filepath.ToSlash(relPath)

		matchedIndices := make(map[int]bool)
		for idx, line := range fileLines {
			if matchFunc(line) {
				matchedIndices[idx] = true
				totalMatches++
				if totalMatches >= limit {
					limitReached = true
					break
				}
			}
		}

		if len(matchedIndices) == 0 {
			return nil
		}

		if contextLines == 0 {
			lineNums := make([]int, 0, len(matchedIndices))
			for idx := range matchedIndices {
				lineNums = append(lineNums, idx)
			}
			sort.Ints(lineNums)
			for _, idx := range lineNums {
				lineContent := truncateGrepLine(fileLines[idx], maxGrepLineLength)
				lineStr := fmt.Sprintf("%s:%d: %s\n", relPath, idx+1, lineContent)
				out.WriteString(lineStr)
				if out.Len() >= maxGrepOutputBytes {
					outputSizeReached = true
					return fs.SkipAll
				}
			}
		} else {
			linesToShow := make(map[int]bool)
			for mIdx := range matchedIndices {
				start := mIdx - contextLines
				if start < 0 {
					start = 0
				}
				end := mIdx + contextLines
				if end >= numLines {
					end = numLines - 1
				}
				for c := start; c <= end; c++ {
					linesToShow[c] = true
				}
			}

			sortedLines := make([]int, 0, len(linesToShow))
			for idx := range linesToShow {
				sortedLines = append(sortedLines, idx)
			}
			sort.Ints(sortedLines)

			prevLine := -1
			for _, idx := range sortedLines {
				if prevLine != -1 && idx > prevLine+1 {
					out.WriteString("--\n")
				}
				lineContent := truncateGrepLine(fileLines[idx], maxGrepLineLength)
				sep := "-"
				if matchedIndices[idx] {
					sep = ":"
				}
				lineStr := fmt.Sprintf("%s:%d%s %s\n", relPath, idx+1, sep, lineContent)
				out.WriteString(lineStr)
				if out.Len() >= maxGrepOutputBytes {
					outputSizeReached = true
					return fs.SkipAll
				}
				prevLine = idx
			}
		}

		if limitReached {
			return fs.SkipAll
		}
		return nil
	}

	if !info.IsDir() {
		_ = processFile(safePath)
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

			if globPattern != "" {
				matched, matchErr := filepath.Match(globPattern, strings.ToLower(name))
				if matchErr == nil && !matched {
					return nil
				}
			}

			return processFile(path)
		})
		if walkErr != nil && walkErr != fs.SkipAll {
			return "", walkErr
		}
	}

	result := out.String()
	if strings.TrimSpace(result) == "" {
		return fmt.Sprintf("No matches found for pattern: %s", pattern), nil
	}

	if limitReached {
		result += fmt.Sprintf("\n[match limit reached: showing first %d matches. Use 'path' or 'glob' to narrow search.]", limit)
	} else if outputSizeReached {
		result += fmt.Sprintf("\n[output size limit reached: %d KB. Use 'path' or 'glob' to narrow search.]", maxGrepOutputBytes/1024)
	}

	return result, nil
}

func truncateGrepLine(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", "")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + " ... [truncated]"
}

func isIgnoredDirName(name string) bool {
	low := strings.ToLower(name)
	switch low {
	case ".git", ".hg", ".svn", "node_modules", "venv", ".venv", "env", ".env",
		"__pycache__", ".pytest_cache", ".tox", ".mypy_cache", ".ruff_cache",
		"dist", "build", "target", "out", "bin", "obj",
		".idea", ".vscode", ".next", ".nuxt", ".cache", "vendor":
		return true
	}
	return strings.HasPrefix(low, ".") && low != "." && low != ".."
}

func isIgnoredFileName(name string) bool {
	low := strings.ToLower(name)
	ext := filepath.Ext(low)
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".iso", ".zip", ".tar", ".gz",
		".7z", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".pdf", ".lock",
		".pyc", ".pyo", ".wasm", ".sqlite", ".db", ".woff", ".woff2", ".ttf":
		return true
	}
	return low == ".ds_store" || low == "thumbs.db"
}

func loadGitIgnore(workspaceRoot string) []string {
	var patterns []string
	gitIgnorePath := filepath.Join(workspaceRoot, ".gitignore")
	data, err := os.ReadFile(gitIgnorePath)
	if err != nil {
		return nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func isIgnoredByGit(path string, workspaceRoot string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)

	for _, pat := range patterns {
		pat = strings.TrimPrefix(pat, "/")
		isDirOnly := strings.HasSuffix(pat, "/")
		cleanPat := strings.TrimSuffix(pat, "/")

		if isDirOnly {
			if strings.Contains(rel, cleanPat+"/") || rel == cleanPat {
				return true
			}
		}

		if matched, _ := filepath.Match(cleanPat, base); matched {
			return true
		}
		if matched, _ := filepath.Match(cleanPat, rel); matched {
			return true
		}
	}
	return false
}
