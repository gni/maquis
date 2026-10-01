package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

func (t *readTool) PromptSnippet() string {
	return "Read file contents"
}

func (t *readTool) PromptGuidelines() []string {
	return []string{"Use 'read' to examine files instead of cat or sed in bash. Do not call 'read' on directory paths; use 'list' to inspect directory trees."}
}

func (t *readTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "read",
			Description: "Read file contents. Supports text files. Optional offset and limit for large files.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to a specific file to read (relative or absolute). Do not pass directory paths; use 'list' to inspect directory trees.",
					},
					"offset": {
						Type:        "number",
						Description: "Line number to start reading from (1-indexed). Optional",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of lines to read. Optional",
					},
				},
				Required: []string{"path"},
			},
		},
	}
}

func (t *readTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path     string `json:"path"`
		File     string `json:"file"`
		FilePath string  `json:"file_path"`
		Offset   float64 `json:"offset"`
		Limit    float64 `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		var rawPath string
		if errStr := json.Unmarshal([]byte(arguments), &rawPath); errStr == nil && strings.TrimSpace(rawPath) != "" {
			args.Path = rawPath
		} else {
			trimmed := strings.TrimSpace(arguments)
			if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
				args.Path = trimmed
			} else {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
		}
	}

	if args.Path == "" {
		if args.FilePath != "" {
			args.Path = args.FilePath
		} else if args.File != "" {
			args.Path = args.File
		}
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

	var entryHeader string
	info, err := os.Stat(safePath)
	if err != nil {
		found := false
		for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go", ".json", ".md", ".yaml", ".yml"} {
			candidate := safePath + ext
			if cInfo, cErr := os.Stat(candidate); cErr == nil && !cInfo.IsDir() {
				safePath = candidate
				info = cInfo
				found = true
				relPath, _ := filepath.Rel(ctx.GetWorkspaceRoot(), candidate)
				if relPath == "" {
					relPath = args.Path + ext
				}
				entryHeader = fmt.Sprintf("[Notice: Resolved '%s' to '%s']\n\n", args.Path, relPath)
				break
			}
		}
		if !found {
			for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go"} {
				if strings.HasSuffix(safePath, ext) {
					trimmed := strings.TrimSuffix(safePath, ext)
					if dInfo, dErr := os.Stat(trimmed); dErr == nil && dInfo.IsDir() {
						safePath = trimmed
						info = dInfo
						found = true
						break
					}
				}
			}
		}
		if !found {
			return "", fmt.Errorf("failed to read file info: %w", err)
		}
	}
	if info.IsDir() {
		foundEntry := false
		for _, entryName := range []string{"__init__.py", "index.ts", "index.js", "index.tsx", "index.jsx", "main.go"} {
			entryPath := filepath.Join(safePath, entryName)
			if entryInfo, err := os.Stat(entryPath); err == nil && !entryInfo.IsDir() && entryInfo.Size() > 0 {
				safePath = entryPath
				info = entryInfo
				foundEntry = true
				relEntry, _ := filepath.Rel(ctx.GetWorkspaceRoot(), entryPath)
				if relEntry == "" {
					relEntry = filepath.Join(args.Path, entryName)
				}
				entryHeader = fmt.Sprintf("[Notice: '%s' is a directory. Automatically reading package entrypoint '%s':]\n\n", args.Path, relEntry)
				break
			}
		}

		if !foundEntry {
			tree, err := ListDirectoryTree(safePath, ctx.GetWorkspaceRoot(), 2, 150)
			if err != nil {
				return "", fmt.Errorf("path '%s' is a directory and failed to list contents: %w", args.Path, err)
			}
			return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n%s", args.Path, tree), nil
		}
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

	_, textWithoutBOM := splitBOM(data)
	contentStr := SanitizeUTF8([]byte(textWithoutBOM))
	if len(data) == 0 || strings.TrimSpace(contentStr) == "" {
		return entryHeader + "(empty file)", nil
	}

	lines := strings.Split(contentStr, "\n")
	offset := int(args.Offset)
	if offset <= 0 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = 1000 // default to 1000 lines so normal files are read completely in one call
	} else if limit > 2000 {
		limit = 2000 // cap maximum lines per read to 2000
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
		result += fmt.Sprintf("\n\n[Showing lines %d to %d of %d. Use read with offset=%d to view more]", offset, end, len(lines), end+1)
	}
	return entryHeader + result, nil
}

type writeTool struct{}

func NewWriteTool() ToolExecutor {
	return &writeTool{}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) PromptSnippet() string {
	return "Create or overwrite complete files"
}

func (t *writeTool) PromptGuidelines() []string {
	return []string{"Use 'write' only for new files or complete rewrites. Never use after an edit mismatch."}
}

func (t *writeTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "write",
			Description: "Create a new file or completely overwrite an existing file. Automatically creates parent directories.",
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
		File         string `json:"file"`
		FilePath     string `json:"file_path"`
		Content      string `json:"content"`
		WriteContent string `json:"write_content"`
		Text         string `json:"text"`
		Body         string `json:"body"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" {
		if args.FilePath != "" {
			args.Path = args.FilePath
		} else if args.File != "" {
			args.Path = args.File
		}
	}
	if args.Path == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	if args.Content == "" {
		if args.WriteContent != "" {
			args.Content = args.WriteContent
		} else if args.Text != "" {
			args.Content = args.Text
		} else if args.Body != "" {
			args.Content = args.Body
		}
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

func (t *editTool) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}

func (t *editTool) PromptGuidelines() []string {
	return []string{
		"Use 'edit' for precise changes (updates[].oldText must match uniquely).",
		"Keep oldText minimal (typically 2-5 lines).",
		"When modifying multiple separate locations in a file, provide multiple updates in updates[] in a single edit call.",
		"If edit reports an oldText mismatch, read the latest file and retry a smaller exact unique block. Never recover by overwriting the existing file with write.",
	}
}

func (t *editTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "edit",
			Description: "Edit a file using exact text replacement blocks. Matches unique blocks against current file contents.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the file to edit.",
					},
					"updates": {
						Type:        "array",
						Description: "One or more targeted replacements.",
						Items: &SchemaProp{
							Type: "object",
							Properties: map[string]SchemaProp{
								"oldText": {
									Type:        "string",
									Description: "Exact unique current text copied from latest read (typically 2-5 lines).",
								},
								"newText": {
									Type:        "string",
									Description: "The replacement text for oldText.",
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
		Path         string        `json:"path"`
		File         string        `json:"file"`
		FilePath     string        `json:"file_path"`
		Updates      []ReplaceEdit `json:"updates"`
		Edits        []ReplaceEdit `json:"edits"`
		Replacements []ReplaceEdit `json:"replacements"`
		OldText      string        `json:"oldText,omitempty"`
		NewText      string        `json:"newText,omitempty"`
		OldTextSnake string        `json:"old_text,omitempty"`
		NewTextSnake string        `json:"new_text,omitempty"`
		OldString    string        `json:"old_string,omitempty"`
		NewString    string        `json:"new_string,omitempty"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Path == "" {
		if args.FilePath != "" {
			args.Path = args.FilePath
		} else if args.File != "" {
			args.Path = args.File
		}
	}
	if args.Path == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	unlock := lockPath(safePath)
	defer unlock()

	edits := args.Updates
	if len(edits) == 0 && len(args.Edits) > 0 {
		edits = args.Edits
	} else if len(edits) == 0 && len(args.Replacements) > 0 {
		edits = args.Replacements
	}

	oldSingle := args.OldText
	if oldSingle == "" {
		if args.OldTextSnake != "" {
			oldSingle = args.OldTextSnake
		} else if args.OldString != "" {
			oldSingle = args.OldString
		}
	}
	newSingle := args.NewText
	if newSingle == "" {
		if args.NewTextSnake != "" {
			newSingle = args.NewTextSnake
		} else if args.NewString != "" {
			newSingle = args.NewString
		}
	}

	if oldSingle != "" {
		edits = append(edits, ReplaceEdit{OldText: oldSingle, NewText: newSingle})
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
	hasBOM, rawStr := splitBOM(data)
	originalEnding := detectLineEnding(rawStr)
	content := strings.ReplaceAll(rawStr, "\r\n", "\n")
	initialContent := content

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
			// Try fuzzy normalized matching (smart quotes, dashes, unicode spaces, trailing whitespace)
			normContent := normalizeForFuzzyMatch(content)
			normOld := normalizeForFuzzyMatch(edit.OldText)
			if normOld != edit.OldText || normContent != content {
				if normIdx := strings.Index(normContent, normOld); normIdx != -1 {
					if strings.Count(normContent, normOld) == 1 {
						startLine := strings.Count(normContent[:normIdx], "\n")
						numLines := strings.Count(normOld, "\n")
						fileLines := strings.Split(content, "\n")
						if startLine+numLines < len(fileLines) {
							actualOldText := strings.Join(fileLines[startLine:startLine+numLines+1], "\n")
							edit.OldText = actualOldText
							indexOfOldText = strings.Index(content, edit.OldText)
						}
					}
				}
			}
		}
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

		updatedContent := strings.Replace(content, edit.OldText, edit.NewText, 1)
		if updatedContent != content {
			contentChanged = true
		}
		content = updatedContent
	}

	if contentChanged {
		diffStr := generateDisplayDiff(initialContent, content, 3)
		diffBuilder.WriteString(diffStr)
		finalStr := content
		if originalEnding == "\r\n" {
			finalStr = strings.ReplaceAll(finalStr, "\n", "\r\n")
		}
		var finalBytes []byte
		if hasBOM {
			finalBytes = append([]byte{0xef, 0xbb, 0xbf}, []byte(finalStr)...)
		} else {
			finalBytes = []byte(finalStr)
		}
		err = os.WriteFile(safePath, finalBytes, 0644)
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

type refMutex struct {
	mu   sync.Mutex
	refs int
}

var (
	fileLocks   = make(map[string]*refMutex)
	fileLocksMu sync.Mutex
)

func lockPath(path string) func() {
	fileLocksMu.Lock()
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	entry, exists := fileLocks[absPath]
	if !exists {
		entry = &refMutex{}
		fileLocks[absPath] = entry
	}
	entry.refs++
	fileLocksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		fileLocksMu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(fileLocks, absPath)
		}
		fileLocksMu.Unlock()
	}
}

func splitBOM(data []byte) (bool, string) {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return true, string(data[3:])
	}
	return false, string(data)
}

func detectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if lf == -1 {
		return "\n"
	}
	if crlf == -1 {
		return "\n"
	}
	if crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func normalizeForFuzzyMatch(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b': // Smart single quotes
			b.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f': // Smart double quotes
			b.WriteRune('"')
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212': // Dashes & minus
			b.WriteRune('-')
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000': // Unicode spaces
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(normalizeForFuzzyMatch(s)), " ")
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

type diffOp int

const (
	diffEqual diffOp = iota
	diffInsert
	diffDelete
)

type diffPart struct {
	op    diffOp
	lines []string
}

func computeMyersDiff(a, b []string) []diffPart {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	if len(a) == 0 {
		return []diffPart{{op: diffInsert, lines: b}}
	}
	if len(b) == 0 {
		return []diffPart{{op: diffDelete, lines: a}}
	}

	prefixLen := 0
	for prefixLen < len(a) && prefixLen < len(b) && a[prefixLen] == b[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < len(a)-prefixLen && suffixLen < len(b)-prefixLen && a[len(a)-1-suffixLen] == b[len(b)-1-suffixLen] {
		suffixLen++
	}

	var parts []diffPart
	if prefixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[:prefixLen]})
	}

	midA := a[prefixLen : len(a)-suffixLen]
	midB := b[prefixLen : len(b)-suffixLen]
	n := len(midA)
	m := len(midB)

	if n == 0 && m == 0 {
		// Nothing in middle
	} else if n == 0 {
		parts = append(parts, diffPart{op: diffInsert, lines: midB})
	} else if m == 0 {
		parts = append(parts, diffPart{op: diffDelete, lines: midA})
	} else if n+m > 2000 {
		parts = append(parts, diffPart{op: diffDelete, lines: midA})
		parts = append(parts, diffPart{op: diffInsert, lines: midB})
	} else {
		maxD := n + m
		offset := maxD
		v := make([]int, 2*maxD+1)
		trace := make([][]int, 0, maxD+1)

		dFound := -1
		for d := 0; d <= maxD; d++ {
			vCopy := make([]int, len(v))
			copy(vCopy, v)
			trace = append(trace, vCopy)

			for k := -d; k <= d; k += 2 {
				var x int
				if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
					x = v[k+1+offset]
				} else {
					x = v[k-1+offset] + 1
				}
				y := x - k
				for x < n && y < m && midA[x] == midB[y] {
					x++
					y++
				}
				v[k+offset] = x
				if x >= n && y >= m {
					dFound = d
					break
				}
			}
			if dFound != -1 {
				break
			}
		}

		if dFound == -1 {
			parts = append(parts, diffPart{op: diffDelete, lines: midA})
			parts = append(parts, diffPart{op: diffInsert, lines: midB})
		} else {
			x := n
			y := m
			var revParts []diffPart

			addRevLine := func(op diffOp, line string) {
				if len(revParts) > 0 && revParts[len(revParts)-1].op == op {
					revParts[len(revParts)-1].lines = append([]string{line}, revParts[len(revParts)-1].lines...)
				} else {
					revParts = append(revParts, diffPart{op: op, lines: []string{line}})
				}
			}

			for d := dFound; d > 0; d-- {
				vSnap := trace[d]
				k := x - y
				var prevK int
				if k == -d || (k != d && vSnap[k-1+offset] < vSnap[k+1+offset]) {
					prevK = k + 1
				} else {
					prevK = k - 1
				}
				prevX := vSnap[prevK+offset]
				prevY := prevX - prevK

				for x > prevX && y > prevY && midA[x-1] == midB[y-1] {
					addRevLine(diffEqual, midA[x-1])
					x--
					y--
				}
				if x == prevX {
					addRevLine(diffInsert, midB[y-1])
					y--
				} else {
					addRevLine(diffDelete, midA[x-1])
					x--
				}
			}
			for x > 0 && y > 0 && midA[x-1] == midB[y-1] {
				addRevLine(diffEqual, midA[x-1])
				x--
				y--
			}

			for i := len(revParts) - 1; i >= 0; i-- {
				parts = append(parts, revParts[i])
			}
		}
	}

	if suffixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[len(a)-suffixLen:]})
	}

	var merged []diffPart
	for _, p := range parts {
		if len(p.lines) == 0 {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].op == p.op {
			merged[len(merged)-1].lines = append(merged[len(merged)-1].lines, p.lines...)
		} else {
			merged = append(merged, p)
		}
	}
	return merged
}

func generateDisplayDiff(oldContent, newContent string, contextLines int) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	parts := computeMyersDiff(oldLines, newLines)
	if len(parts) == 0 {
		return ""
	}

	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	width := len(strconv.Itoa(maxLineNum))
	if width < 4 {
		width = 4
	}

	var sb strings.Builder
	oldLineNum := 1
	newLineNum := 1
	lastWasChange := false

	for i := 0; i < len(parts); i++ {
		part := parts[i]

		if part.op == diffInsert || part.op == diffDelete {
			for _, line := range part.lines {
				if part.op == diffInsert {
					sb.WriteString(fmt.Sprintf("\x1b[32m%-*d + %s\x1b[0m\n", width, newLineNum, line))
					newLineNum++
				} else {
					sb.WriteString(fmt.Sprintf("\x1b[31m%-*d - %s\x1b[0m\n", width, oldLineNum, line))
					oldLineNum++
				}
			}
			lastWasChange = true
		} else {
			raw := part.lines
			nextPartIsChange := i < len(parts)-1 && (parts[i+1].op == diffInsert || parts[i+1].op == diffDelete)
			hasLeadingChange := lastWasChange
			hasTrailingChange := nextPartIsChange

			if hasLeadingChange && hasTrailingChange {
				if len(raw) <= contextLines*2 {
					for _, line := range raw {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}
				} else {
					leadingLines := raw[:contextLines]
					trailingLines := raw[len(raw)-contextLines:]
					skippedLines := len(raw) - len(leadingLines) - len(trailingLines)

					for _, line := range leadingLines {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}

					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines

					for _, line := range trailingLines {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}
				}
			} else if hasLeadingChange {
				shownLines := raw
				if len(shownLines) > contextLines {
					shownLines = raw[:contextLines]
				}
				skippedLines := len(raw) - len(shownLines)

				for _, line := range shownLines {
					sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
					oldLineNum++
					newLineNum++
				}

				if skippedLines > 0 {
					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines
				}
			} else if hasTrailingChange {
				skippedLines := len(raw) - contextLines
				if skippedLines < 0 {
					skippedLines = 0
				}
				if skippedLines > 0 {
					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines
				}

				for _, line := range raw[skippedLines:] {
					sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
					oldLineNum++
					newLineNum++
				}
			} else {
				oldLineNum += len(raw)
				newLineNum += len(raw)
			}

			lastWasChange = false
		}
	}

	return sb.String()
}

