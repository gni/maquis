package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

type ReplaceEdit struct {
	OldText      string `json:"oldText"`
	NewText      string `json:"newText"`
	OldTextSnake string `json:"old_text"`
	NewTextSnake string `json:"new_text"`
	OldString    string `json:"old_string"`
	NewString    string `json:"new_string"`
}

type readTool struct{}

func NewReadTool() ToolExecutor {
	return &readTool{}
}

func (t *readTool) Name() string { return "read" }

func (t *readTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "read",
			Description: "Read the contents of a file. IMPORTANT: Do NOT use this tool to find or extract function, class, struct, or method definitions. Use the 'explore' tool for that instead. Only use 'read' to view imports, config files, or full contents when preparing to edit.",
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

func (t *readTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	if hasIgnoredComponent(args.Path) {
		return "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", args.Path)
	}

	unlock := lockPath(safePath)
	defer unlock()

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file info: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("cannot read: path '%s' is a directory", args.Path)
	}
	if info.Size() > 500*1024 { // 500KB limit
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is 500KB", info.Size())
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Check if file is binary
	if isBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files")
	}

	contentStr := SanitizeUTF8(data)
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

	limit := args.Limit
	if limit <= 0 {
		limit = 500 // default to 500 lines to prevent token blowup
	} else if limit > 1000 {
		limit = 1000 // cap maximum lines per read to 1000
	}

	end := offset + limit - 1
	truncated := false
	if end > len(lines) {
		end = len(lines)
	} else if end < len(lines) {
		truncated = true
	}

	var resultLines []string
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > 1000 {
			line = line[:1000] + " ... [line truncated: line is too long] ..."
		}
		resultLines = append(resultLines, line)
	}

	result := strings.Join(resultLines, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[File truncated. Showing lines %d-%d of %d. Use offset=%d to read the next section.]", offset, end, len(lines), end+1)
	}
	return result, nil
}

type writeTool struct{}

func NewWriteTool() ToolExecutor {
	return &writeTool{}
}

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

func (t *writeTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path         string `json:"path"`
		Content      string `json:"content"`
		WriteContent string `json:"write_content"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Content == "" && args.WriteContent != "" {
		args.Content = args.WriteContent
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	unlock := lockPath(safePath)
	defer unlock()

	// Code Omission Protection
	if placeholders := DetectOmissionPlaceholders(args.Content); len(placeholders) > 0 {
		return "", fmt.Errorf("refusing to write file: detected code omission placeholder(s) like: %q. Please provide the complete file content without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	err = os.WriteFile(safePath, []byte(args.Content), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	ctx.ReloadSkills()
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path), nil
}

type editTool struct{}

func NewEditTool() ToolExecutor {
	return &editTool{}
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "edit",
			Description: "Edit a single file using exact search and replace text blocks. oldText must match exactly. oldText cannot be empty (to insert text, match the preceding/following line and include it in newText).",
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

func (t *editTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path    string        `json:"path"`
		Edits   []ReplaceEdit `json:"updates"`
		OldText string        `json:"oldText,omitempty"`
		NewText string        `json:"newText,omitempty"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	unlock := lockPath(safePath)
	defer unlock()

	edits := args.Edits
	if args.OldText != "" && args.NewText != "" {
		edits = append(edits, ReplaceEdit{OldText: args.OldText, NewText: args.NewText})
	}

	// Code Omission Protection
	for _, edit := range edits {
		if placeholders := DetectOmissionPlaceholders(edit.NewText); len(placeholders) > 0 {
			return "", fmt.Errorf("refusing to edit file: detected code omission placeholder(s) in replacement text: %q. Please provide the complete new code replacement block without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
		}
	}

	if len(edits) == 0 {
		return "", fmt.Errorf("no edits specified to apply")
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	var diffBuilder strings.Builder
	for i := range edits {
		edit := &edits[i]
		if edit.OldText == "" {
			if edit.OldTextSnake != "" {
				edit.OldText = edit.OldTextSnake
			} else if edit.OldString != "" {
				edit.OldText = edit.OldString
			}
		}
		if edit.NewText == "" {
			if edit.NewTextSnake != "" {
				edit.NewText = edit.NewTextSnake
			} else if edit.NewString != "" {
				edit.NewText = edit.NewString
			}
		}

		edit.OldText = strings.ReplaceAll(edit.OldText, "\r\n", "\n")
		edit.NewText = strings.ReplaceAll(edit.NewText, "\r\n", "\n")

		if strings.TrimSpace(edit.OldText) == "" {
			return "", fmt.Errorf("edit[%d]: oldText cannot be empty or just whitespace. If you want to insert new text, you must include some existing surrounding text in oldText, and replicate it in newText alongside your insertion.", i)
		}

		indexOfOldText := strings.Index(content, edit.OldText)
		if indexOfOldText == -1 {
			// Try resilient line-by-line whitespace-insensitive matching
			oldLines := strings.Split(edit.OldText, "\n")
			var cleanOldLines []string
			for _, l := range oldLines {
				cleanOldLines = append(cleanOldLines, strings.TrimSpace(l))
			}

			// Strip leading and trailing empty lines from cleanOldLines to find the core matching block
			startIdx := 0
			for startIdx < len(cleanOldLines) && cleanOldLines[startIdx] == "" {
				startIdx++
			}
			endIdx := len(cleanOldLines)
			for endIdx > startIdx && cleanOldLines[endIdx-1] == "" {
				endIdx--
			}
			coreOldLines := cleanOldLines[startIdx:endIdx]

			if len(coreOldLines) > 0 {
				fileLines := strings.Split(content, "\n")
				matchStart := -1
				matchEnd := -1
				matchesCount := 0

				for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
					matched := true
					for j := 0; j < len(coreOldLines); j++ {
						fileLineNormalized := normalizeSpace(fileLines[fs+j])
						oldLineNormalized := normalizeSpace(coreOldLines[j])
						if fileLineNormalized != oldLineNormalized {
							matched = false
							break
						}
					}
					if matched {
						matchStart = fs
						matchEnd = fs + len(coreOldLines)
						matchesCount++
					}
				}

				if matchesCount == 1 {
					// Found a unique resilient match!
					// Expand back to include leading/trailing empty lines matching original request
					actualStart := matchStart
					for actualStart > 0 && matchStart-actualStart < startIdx {
						if strings.TrimSpace(fileLines[actualStart-1]) == "" {
							actualStart--
						} else {
							break
						}
					}
					actualEnd := matchEnd
					for actualEnd < len(fileLines) && actualEnd-matchEnd < (len(cleanOldLines) - endIdx) {
						if strings.TrimSpace(fileLines[actualEnd]) == "" {
							actualEnd++
						} else {
							break
						}
					}
					actualOldText := strings.Join(fileLines[actualStart:actualEnd], "\n")
					edit.OldText = actualOldText
					indexOfOldText = strings.Index(content, edit.OldText)
				}
			}
		}

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
	ctx.ReloadSkills()

	return diffBuilder.String(), nil
}

type lsTool struct{}

func NewLsTool() ToolExecutor {
	return &lsTool{}
}

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
					"recursive": {
						Type:        "boolean",
						Description: "List all files and directories recursively.",
					},
				},
				Required: []string{},
			},
		},
	}
}

func (t *lsTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		// ignore JSON syntax errors
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	if !args.Recursive {
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

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Contents of %s (recursive):\n", searchPath))
	patterns := loadGitignorePatterns(ctx.GetWorkspaceRoot())
	count := 0
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

		if relPath == "." {
			return nil
		}

		if count >= 500 {
			return fmt.Errorf("limit_reached")
		}

		typeStr := "file"
		if info.IsDir() {
			typeStr = "dir "
		}

		sb.WriteString(fmt.Sprintf("  [%s]  %-35s  %d bytes\n", typeStr, relPath, info.Size()))
		count++
		return nil
	})
	if err != nil && err.Error() == "limit_reached" {
		sb.WriteString("\n[Listing truncated. Too many files found (limit: 500). Use recursive: false or search a subdirectory.]\n")
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to walk directory: %w", err)
	}
	return sb.String(), nil
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

func SanitizeUTF8(data []byte) string {
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

var omissionRegexes = []*regexp.Regexp{
	// Matches lines containing "rest of code", "rest of method(s)", "unchanged code", etc.
	regexp.MustCompile(`(?i)(?:rest of|unchanged|same as|original|existing)\s+(?:code|methods?|functions?|class(?:es)?|files?|implementations?)\s*\.{3,}`),
	// Matches lines with just comments and dots: e.g. // ... or # ... or /* ... */
	regexp.MustCompile(`(?i)^\s*(?://|#|/\*)\s*\.{3,}\s*(?:\*/)?\s*$`),
	// Matches brackets with dots: (...)
	regexp.MustCompile(`^\s*\(\s*\.{3,}\s*\)\s*$`),
	// Matches TODO comments that suggest omission: e.g. // TODO: implement the rest or // TODO ...
	regexp.MustCompile(`(?i)(?://|#|/\*)\s*todo\s*[\:\-\s]*\.*(?:\s*(?:implement|add|write)\s+(?:the\s+)?(rest|remaining|code|methods?))?\s*\.{3,}`),
}

// DetectOmissionPlaceholders searches for code omission comments like '// ... rest of code'.
func DetectOmissionPlaceholders(text string) []string {
	var matches []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, rx := range omissionRegexes {
			if rx.MatchString(trimmed) {
				matches = append(matches, line)
				break
			}
		}
	}
	return matches
}

var (
	fileLocks   = make(map[string]*sync.Mutex)
	fileLocksMu sync.Mutex
)

func lockPath(path string) func() {
	fileLocksMu.Lock()
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	mu, exists := fileLocks[absPath]
	if !exists {
		mu = &sync.Mutex{}
		fileLocks[absPath] = mu
	}
	fileLocksMu.Unlock()

	mu.Lock()
	return func() {
		mu.Unlock()
	}
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func isIgnored(name string, isDir bool) bool {
	if isDir {
		low := strings.ToLower(name)
		if low == "node_modules" || low == "venv" || low == ".venv" || low == ".git" || low == "__pycache__" || low == ".idea" || low == ".vscode" || low == "build" || low == "dist" || low == "target" || low == "tmp" || low == "temp" || low == "vendor" || low == ".yarn" || low == ".cache" {
			return true
		}
	} else {
		ext := filepath.Ext(name)
		if ext == ".pyc" || ext == ".pyo" || ext == ".pyd" || name == ".gitignore" || name == "package-lock.json" || name == "yarn.lock" || name == "pnpm-lock.yaml" || name == ".DS_Store" || name == "pnpm-workspace.yaml" || ext == ".tsbuildinfo" || name == ".eslintcache" {
			return true
		}
	}
	return false
}

func loadGitignorePatterns(workspaceRoot string) []string {
	gitignorePath := filepath.Join(workspaceRoot, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		return nil
	}

	var patterns []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func matchesGitignore(path string, isDir bool, patterns []string) bool {
	path = filepath.ToSlash(path)
	for _, p := range patterns {
		p = filepath.ToSlash(p)
		if p == "" {
			continue
		}

		isDirPattern := strings.HasSuffix(p, "/")
		cleanPattern := strings.TrimSuffix(p, "/")

		parts := strings.Split(path, "/")
		for _, part := range parts {
			if part == cleanPattern {
				if !isDirPattern || isDir {
					return true
				}
			}
			if strings.Contains(cleanPattern, "*") {
				if matched, _ := filepath.Match(cleanPattern, part); matched {
					if !isDirPattern || isDir {
						return true
					}
				}
			}
		}

		if strings.HasSuffix(path, cleanPattern) {
			if !isDirPattern || isDir {
				return true
			}
		}
	}
	return false
}

func isCompiledOrLockfile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".pyc" || ext == ".pyo" || ext == ".pyd" || ext == ".o" || ext == ".a" || ext == ".so" || ext == ".dll" || ext == ".dylib" || ext == ".exe" || ext == ".class" || ext == ".jar" || ext == ".zip" || ext == ".tar" || ext == ".gz" {
		return true
	}
	lowName := strings.ToLower(name)
	if lowName == "package-lock.json" || lowName == "yarn.lock" || lowName == "pnpm-lock.yaml" || lowName == "composer.lock" || lowName == "poetry.lock" || lowName == "cargo.lock" {
		return true
	}
	return false
}

func hasIgnoredComponent(path string) bool {
	path = filepath.Clean(path)
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		low := strings.ToLower(part)
		if low == "node_modules" || low == "venv" || low == ".venv" || low == ".git" || low == "__pycache__" {
			return true
		}
	}
	return false
}
