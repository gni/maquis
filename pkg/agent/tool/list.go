package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type listTool struct{}

// NewListTool creates a new directory listing tool.
func NewListTool() ToolExecutor {
	return &listTool{}
}

func (t *listTool) Name() string { return "list" }

func (t *listTool) PromptSnippet() string {
	return "List directory contents"
}

func (t *listTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "list",
			Description: "List directory contents",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Directory to list (default: current directory)",
					},
					"depth": {
						Type:        "number",
						Description: "Directory depth (default: 3)",
					},
				},
			},
		},
	}
}

func (t *listTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path     string `json:"path"`
		Dir      string `json:"dir"`
		DirPath  string  `json:"dir_path"`
		Depth    float64 `json:"depth"`
	}

	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			args.Path = unquoted
		}
	} else if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
			args.Path = trimmed
		} else if trimmed != "" && trimmed != "{}" {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}

	targetPath := args.Path
	if targetPath == "" {
		if args.Dir != "" {
			targetPath = args.Dir
		} else if args.DirPath != "" {
			targetPath = args.DirPath
		}
	}
	if targetPath == "" {
		targetPath = "."
	}

	depth := int(args.Depth)
	if depth <= 0 {
		depth = 3
	} else if depth > 4 {
		depth = 4
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		// If path attempts to escape workspace root (e.g. '..' or parent directory),
		// gracefully list workspace root instead of failing with an error.
		safePath = ctx.GetWorkspaceRoot()
		tree, treeErr := ListDirectoryTree(safePath, safePath, depth, 150)
		if treeErr != nil {
			return "", err
		}
		return fmt.Sprintf("[Notice: '%s' is outside workspace root. Showing workspace root ('%s') instead:]\n\n%s", targetPath, safePath, tree), nil
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", targetPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path '%s' is a file, not a directory. Use the 'read' tool to view its contents", targetPath)
	}

	return ListDirectoryTree(safePath, ctx.GetWorkspaceRoot(), depth, 150)
}

// ListDirectoryTree traverses and renders a clean visual ASCII tree of the directory,
// automatically filtering out dependency, cache, and build directories.
func ListDirectoryTree(dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 5 {
		maxDepth = 5
	}
	if maxEntries <= 0 {
		maxEntries = 150
	}

	gitIgnorePatterns := loadGitIgnore(workspaceRoot)

	relBase, err := filepath.Rel(workspaceRoot, dirPath)
	var header string
	if err != nil || relBase == "." || relBase == "" {
		header = "./\n"
	} else {
		header = filepath.ToSlash(relBase) + "/\n"
	}

	var sb strings.Builder
	sb.WriteString(header)

	entryCount := 0
	limitReached := false

	var walkTree func(currentDir string, prefix string, depth int)
	walkTree = func(currentDir string, prefix string, depth int) {
		if depth > maxDepth || limitReached {
			return
		}

		entries, err := os.ReadDir(currentDir)
		if err != nil {
			return
		}

		var validEntries []os.DirEntry
		for _, entry := range entries {
			name := entry.Name()
			fullPath := filepath.Join(currentDir, name)

			if entry.IsDir() {
				if isIgnoredDirName(name) || isIgnoredByGit(fullPath, workspaceRoot, gitIgnorePatterns) {
					continue
				}
			} else {
				if isIgnoredFileName(name) || isIgnoredByGit(fullPath, workspaceRoot, gitIgnorePatterns) {
					continue
				}
			}
			validEntries = append(validEntries, entry)
		}

		sort.Slice(validEntries, func(i, j int) bool {
			if validEntries[i].IsDir() != validEntries[j].IsDir() {
				return validEntries[i].IsDir()
			}
			return strings.ToLower(validEntries[i].Name()) < strings.ToLower(validEntries[j].Name())
		})

		for i, entry := range validEntries {
			if entryCount >= maxEntries {
				limitReached = true
				return
			}
			entryCount++

			isLast := i == len(validEntries)-1
			connector := "├── "
			childPrefix := prefix + "│   "
			if isLast {
				connector = "└── "
				childPrefix = prefix + "    "
			}

			name := entry.Name()
			if entry.IsDir() {
				sb.WriteString(prefix + connector + name + "/\n")
				walkTree(filepath.Join(currentDir, name), childPrefix, depth+1)
			} else {
				sb.WriteString(prefix + connector + name + "\n")
			}
		}
	}

	walkTree(dirPath, "", 1)

	if limitReached {
		sb.WriteString(fmt.Sprintf("\n[Listing capped at %d entries. Use 'path' with a subfolder to view deeper.]", maxEntries))
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}
