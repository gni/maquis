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
			Description: "Read the contents of a file. If the file is truncated, you MUST call this tool again with the 'offset' parameter to read the remaining contents.",
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
		result += fmt.Sprintf("\n\n[File truncated. Showing lines %d-%d of %d. IMPORTANT: You MUST call the read tool again with offset=%d to read the next section!]", offset, end, len(lines), end+1)
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
			Description: "Create a new file or intentionally replace a complete file. Never overwrite an existing file merely to recover from an edit oldText mismatch; read it again and retry a smaller exact edit.",
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
			Description: "Edit one file using exact, unique search-and-replace blocks copied from the latest read. If oldText is stale, read the file again and retry a smaller unique block instead of overwriting the whole file.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the file to edit.",
					},
					"updates": {
						Type:        "array",
						Description: "List of replacement blocks.",
						Items: &SchemaProp{
							Type: "object",
							Properties: map[string]SchemaProp{
								"oldText": {
									Type:        "string",
									Description: "The exact text to be replaced. NEVER use an empty string. To insert text, you MUST include the adjacent existing text in this field, and reproduce it in newText alongside your insertion.",
								},
								"newText": {
									Type:        "string",
									Description: "The replacement text.",
								},
							},
							Required: []string{"oldText", "newText"},
						},
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
	contentChanged := false
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
					for actualEnd < len(fileLines) && actualEnd-matchEnd < (len(cleanOldLines)-endIdx) {
						if strings.TrimSpace(fileLines[actualEnd]) == "" {
							actualEnd++
						} else {
							break
						}
					}
					actualOldText := strings.Join(fileLines[actualStart:actualEnd], "\n")
					edit.OldText = actualOldText
					indexOfOldText = strings.Index(content, edit.OldText)
				} else if matchesCount == 0 && len(coreOldLines) >= 4 {
					// Fuzzy block matcher: find a unique window matching >= 80% of lines for blocks >= 4 lines
					bestMatchStart := -1
					bestMatchScore := 0
					bestMatchCount := 0

					for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
						score := 0
						for j := 0; j < len(coreOldLines); j++ {
							fNorm := normalizeSpace(fileLines[fs+j])
							oNorm := normalizeSpace(coreOldLines[j])
							if fNorm == oNorm || strings.Contains(fNorm, oNorm) || strings.Contains(oNorm, fNorm) {
								score++
							}
						}
						minScore := (len(coreOldLines) * 4) / 5
						if minScore < 3 {
							minScore = 3
						}
						if score >= minScore {
							if score > bestMatchScore {
								bestMatchScore = score
								bestMatchStart = fs
								bestMatchCount = 1
							} else if score == bestMatchScore {
								bestMatchCount++
							}
						}
					}

					if bestMatchCount == 1 && bestMatchStart >= 0 {
						actualOldText := strings.Join(fileLines[bestMatchStart:bestMatchStart+len(coreOldLines)], "\n")
						edit.OldText = actualOldText
						indexOfOldText = strings.Index(content, edit.OldText)
					}
				}
			}
		}

		if indexOfOldText == -1 {
			if replacementAlreadyApplied(content, edit.NewText) {
				diffBuilder.WriteString(fmt.Sprintf("edit[%d]: requested replacement already present; file unchanged\n", i))
				continue
			}
			closestLine := findClosestLineMatch(content, edit.OldText)
			if closestLine > 0 {
				return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nRecommendation: A similar block was detected around line %d. Read the file again starting at offset=%d using 'read', copy a small unique 2-5 line block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write.", i, args.Path, closestLine, closestLine)
			}
			return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nRecommendation: Read the file again using 'read' to verify its current contents, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write.", i, args.Path)
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
		updatedContent := strings.Replace(content, edit.OldText, edit.NewText, 1)
		if updatedContent != content {
			contentChanged = true
		}
		content = updatedContent

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

	if contentChanged {
		err = os.WriteFile(safePath, []byte(content), 0644)
		if err != nil {
			return "", fmt.Errorf("failed to write modified content back: %w", err)
		}
		ctx.ReloadSkills()
	}

	return diffBuilder.String(), nil
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

func replacementAlreadyApplied(content, newText string) bool {
	trimmed := strings.TrimSpace(newText)
	if trimmed == "" {
		return false
	}
	if len(trimmed) < 16 && !strings.Contains(newText, "\n") {
		return false
	}
	return strings.Count(content, newText) == 1
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

func findClosestLineMatch(content, oldText string) int {
	fileLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")
	var coreLine string
	for _, l := range oldLines {
		t := strings.TrimSpace(l)
		if len(t) > 3 {
			coreLine = t
			break
		}
	}
	if coreLine == "" {
		return 0
	}
	coreNorm := normalizeSpace(coreLine)
	for idx, fLine := range fileLines {
		if strings.Contains(fLine, coreLine) || normalizeSpace(fLine) == coreNorm {
			return idx + 1
		}
	}
	return 0
}
