package tool

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type exploreTool struct{}

func NewExploreTool() ToolExecutor {
	return &exploreTool{}
}

func (t *exploreTool) Name() string { return "explore" }

func (t *exploreTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "explore",
			Description: "Explore the codebase by searching for symbol definitions (functions, classes, structs, methods). Returns the signature and body of the matched symbols.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"query": {
						Type:        "string",
						Description: "Symbol name to search for (e.g., 'NewAgent', 'MyClass').",
					},
					"path": {
						Type:        "string",
						Description: "Directory or file to limit search (default: current directory).",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of symbols to return (default: 10).",
					},
				},
				Required: []string{"query"},
			},
		},
	}
}

func (t *exploreTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
		Limit int    `json:"limit"`
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
		limit = 10
	}

	queryLower := strings.ToLower(args.Query)

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
		if isDir {
			return nil
		}

		ext := filepath.Ext(path)
		if ext == ".go" {
			res := exploreGoFile(path, queryLower)
			if len(res) > 0 {
				relToRoot, _ := filepath.Rel(ctx.GetWorkspaceRoot(), path)
				for _, r := range res {
					results = append(results, fmt.Sprintf("File: %s\n%s", relToRoot, r))
					if len(results) >= limit {
						return fmt.Errorf("limit_reached")
					}
				}
			}
		} else if ext == ".ts" || ext == ".js" || ext == ".py" || ext == ".java" {
			res := exploreRegexFile(path, queryLower, ext)
			if len(res) > 0 {
				relToRoot, _ := filepath.Rel(ctx.GetWorkspaceRoot(), path)
				for _, r := range res {
					results = append(results, fmt.Sprintf("File: %s\n%s", relToRoot, r))
					if len(results) >= limit {
						return fmt.Errorf("limit_reached")
					}
				}
			}
		}

		return nil
	})

	if err != nil && err.Error() != "limit_reached" {
		return "", fmt.Errorf("explore failed: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No symbols found matching '%s'.", args.Query), nil
	}

	return strings.Join(results, "\n\n------------------\n\n"), nil
}

func exploreGoFile(path string, queryLower string) []string {
	var found []string
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return found
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return found
	}
	content := string(contentBytes)

	extractBody := func(node ast.Node) string {
		start := fset.Position(node.Pos()).Offset
		end := fset.Position(node.End()).Offset
		if start >= 0 && end <= len(content) && start < end {
			return content[start:end]
		}
		return ""
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if strings.Contains(strings.ToLower(name), queryLower) {
				body := extractBody(d)
				doc := ""
				if d.Doc != nil {
					doc = d.Doc.Text()
				}
				if doc != "" {
					body = "// " + strings.ReplaceAll(strings.TrimSpace(doc), "\n", "\n// ") + "\n" + body
				}
				found = append(found, fmt.Sprintf("Function: %s\n```go\n%s\n```", name, body))
			}
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						name := ts.Name.Name
						if strings.Contains(strings.ToLower(name), queryLower) {
							body := extractBody(d)
							doc := ""
							if d.Doc != nil {
								doc = d.Doc.Text()
							}
							if doc != "" {
								body = "// " + strings.ReplaceAll(strings.TrimSpace(doc), "\n", "\n// ") + "\n" + body
							}
							found = append(found, fmt.Sprintf("Type: %s\n```go\n%s\n```", name, body))
						}
					}
				}
			}
		}
	}
	return found
}

func exploreRegexFile(path string, queryLower string, ext string) []string {
	var found []string
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return found
	}
	lines := strings.Split(string(contentBytes), "\n")

	var re *regexp.Regexp
	isPython := false
	switch ext {
	case ".py":
		isPython = true
		re = regexp.MustCompile(`^(\s*)(def|class|async\s+def)\s+([a-zA-Z0-9_]+)`)
	case ".js", ".ts":
		re = regexp.MustCompile(`^\s*(export\s+)?(default\s+)?(async\s+)?(function|class)\s+([a-zA-Z0-9_]+)|\s*(export\s+)?(const|let|var)\s+([a-zA-Z0-9_]+)\s*=\s*(async\s+)?(\([^)]*\)\s*=>|function)`)
	case ".java":
		re = regexp.MustCompile(`^\s*(public|private|protected)?\s*(static)?\s*(final)?\s*(class|interface|record|enum)\s+([a-zA-Z0-9_]+)`)
	default:
		return found
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		matches := re.FindStringSubmatch(line)
		if len(matches) > 0 {
			var name string
			var indent string
			if isPython {
				indent = matches[1]
				name = matches[3]
			} else {
				for _, m := range matches[1:] {
					mTrim := strings.TrimSpace(m)
					if mTrim != "" && mTrim != "function" && mTrim != "class" && mTrim != "export" && mTrim != "default" && mTrim != "async" && mTrim != "public" && mTrim != "private" && mTrim != "protected" && mTrim != "static" && mTrim != "final" && mTrim != "interface" && mTrim != "record" && mTrim != "enum" && mTrim != "const" && mTrim != "let" && mTrim != "var" && !strings.Contains(mTrim, "=>") {
						name = mTrim
						break
					}
				}
			}

			if name != "" && strings.Contains(strings.ToLower(name), queryLower) {
				start := i
				end := i + 1

				if isPython {
					// Python: extract block based on indentation
					for end < len(lines) {
						if strings.TrimSpace(lines[end]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end]), "#") {
							end++
							continue
						}
						if !strings.HasPrefix(lines[end], indent+" ") && !strings.HasPrefix(lines[end], indent+"\t") {
							break
						}
						end++
					}
				} else {
					// Braced languages: count braces
					openBraces := strings.Count(line, "{")
					closeBraces := strings.Count(line, "}")
					balance := openBraces - closeBraces

					// Wait for the first open brace if it's not on the same line
					for end < len(lines) && balance == 0 && !strings.Contains(lines[end-1], "{") {
						openBraces += strings.Count(lines[end], "{")
						closeBraces += strings.Count(lines[end], "}")
						balance += strings.Count(lines[end], "{") - strings.Count(lines[end], "}")
						end++
					}

					if balance > 0 {
						for end < len(lines) {
							openBraces += strings.Count(lines[end], "{")
							closeBraces += strings.Count(lines[end], "}")
							balance += strings.Count(lines[end], "{") - strings.Count(lines[end], "}")
							end++
							if balance <= 0 {
								break
							}
						}
					} else {
						// Single line or no braces found
						end = i + 1
					}
				}

				if end > len(lines) {
					end = len(lines)
				}
				body := strings.Join(lines[start:end], "\n")
				found = append(found, fmt.Sprintf("Symbol: %s\n```%s\n%s\n```", name, strings.TrimPrefix(ext, "."), body))
				i = end - 1 // skip ahead
			}
		}
	}

	return found
}
