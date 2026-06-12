package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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

// LoadMemoryContext loads global (~/.bidouille/BIDOUILLE.md) and project (MEMORY.md) memory context.
func (a *Agent) LoadMemoryContext() string {
	var sb strings.Builder

	// 1. Global Memory (~/.bidouille/BIDOUILLE.md)
	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, ".bidouille", "BIDOUILLE.md")
		if data, err := os.ReadFile(globalPath); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if len(trimmed) > 0 {
				sb.WriteString(fmt.Sprintf("\n\n--- Global Memory & Personalization Context (%s) ---\n%s\n--- End Global Memory ---", globalPath, trimmed))
			}
		}
	}

	// 2. Project Memory (current workspace MEMORY.md)
	// We search in current directory or traverse up to git root or stop at workspace root
	wd, err := os.Getwd()
	if err == nil {
		dir := wd
		for {
			projectPath := filepath.Join(dir, "MEMORY.md")
			if data, err := os.ReadFile(projectPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\n--- Project Memory & Learnings (%s) ---\n%s\n--- End Project Memory ---", projectPath, trimmed))
				}
				break
			}

			projectDotPath := filepath.Join(dir, ".bidouille", "MEMORY.md")
			if data, err := os.ReadFile(projectDotPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\n--- Project Memory & Learnings (%s) ---\n%s\n--- End Project Memory ---", projectDotPath, trimmed))
				}
				break
			}

			// Stop if we reach git root, workspace root, or root directory
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			if dir == a.WorkspaceRoot {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return sb.String()
}
