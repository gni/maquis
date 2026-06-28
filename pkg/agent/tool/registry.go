package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type SchemaProp struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Enum        []string              `json:"enum,omitempty"`
	Items       *SchemaProp           `json:"items,omitempty"`
	Properties  map[string]SchemaProp `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
}

type JSONSchema struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

func (j JSONSchema) MarshalJSON() ([]byte, error) {
	var propsParts []string

	orderedKeys := []string{"path", "command"}
	for _, key := range orderedKeys {
		if prop, ok := j.Properties[key]; ok {
			propBytes, err := json.Marshal(prop)
			if err != nil {
				return nil, err
			}
			propsParts = append(propsParts, fmt.Sprintf("%q:%s", key, string(propBytes)))
		}
	}

	var otherKeys []string
	for k := range j.Properties {
		isOrdered := false
		for _, ok := range orderedKeys {
			if k == ok {
				isOrdered = true
				break
			}
		}
		if !isOrdered {
			otherKeys = append(otherKeys, k)
		}
	}
	sort.Strings(otherKeys)

	for _, k := range otherKeys {
		propBytes, err := json.Marshal(j.Properties[k])
		if err != nil {
			return nil, err
		}
		propsParts = append(propsParts, fmt.Sprintf("%q:%s", k, string(propBytes)))
	}

	propsJSON := "{" + strings.Join(propsParts, ",") + "}"

	var requiredJSON string
	if len(j.Required) > 0 {
		reqBytes, err := json.Marshal(j.Required)
		if err != nil {
			return nil, err
		}
		requiredJSON = fmt.Sprintf(",%q:%s", "required", string(reqBytes))
	}

	fullJSON := fmt.Sprintf("{%q:%q,%q:%s%s}", "type", j.Type, "properties", propsJSON, requiredJSON)
	return []byte(fullJSON), nil
}

type FunctionDefinition struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

type AgentContext interface {
	SafePath(inputPath string) (string, error)
	GetWorkspaceRoot() string
	GetActiveSkills() []Skill
	ReloadSkills() []Skill
	SpawnTask(command string, w io.Writer) (string, error)
	GetTaskStatus(taskID string) (string, string, error)
	KillTask(taskID string) error
	Context() context.Context
	HasSubagent(name string) bool
}

type ToolExecutor interface {
	Name() string
	Definition() Tool
	Execute(ctx AgentContext, arguments string) (string, error)
}

type ToolRegistry struct {
	tools map[string]ToolExecutor
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]ToolExecutor)}
}

func (r *ToolRegistry) Register(t ToolExecutor) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) UnregisterPrefix(prefix string) {
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
		}
	}
}

func (r *ToolRegistry) Execute(ctx AgentContext, name string, arguments string) (string, error) {
	executor, exists := r.tools[name]
	if !exists {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	var temp interface{}
	// 1. Try parsing the raw arguments first
	if err := json.Unmarshal([]byte(arguments), &temp); err == nil {
		return executor.Execute(ctx, arguments)
	}

	// 2. If it fails, try repairing
	repairedArgs := repairJSON(arguments)
	if err := json.Unmarshal([]byte(repairedArgs), &temp); err == nil {
		return executor.Execute(ctx, repairedArgs)
	} else {
		return "", fmt.Errorf("JSON validation failed: %w.\nParameters generated: %s\nRecommendation: Please output valid JSON. Double-check closing braces, ensure internal double-quotes are escaped, and verify that newlines inside strings are escaped as '\\n'.", err, arguments)
	}
}

func repairJSON(js string) string {
	js = strings.TrimSpace(js)
	if js == "" {
		return "{}"
	}

	// 1. Strip markdown code block wrappers if present
	if strings.HasPrefix(js, "```") {
		lines := strings.Split(js, "\n")
		var cleanLines []string
		for _, line := range lines {
			trimmedLine := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmedLine, "```") {
				cleanLines = append(cleanLines, line)
			}
		}
		js = strings.Join(cleanLines, "\n")
		js = strings.TrimSpace(js)
	}

	// 2. Fix extra closing braces/brackets at the end
	for len(js) > 0 {
		openBraces := strings.Count(js, "{")
		closeBraces := strings.Count(js, "}")
		if closeBraces > openBraces && strings.HasSuffix(js, "}") {
			js = strings.TrimSuffix(js, "}")
			js = strings.TrimSpace(js)
		} else {
			break
		}
	}
	for len(js) > 0 {
		openBrackets := strings.Count(js, "[")
		closeBrackets := strings.Count(js, "]")
		if closeBrackets > openBrackets && strings.HasSuffix(js, "]") {
			js = strings.TrimSuffix(js, "]")
			js = strings.TrimSpace(js)
		} else {
			break
		}
	}

	// 3. Fix missing closing brackets/braces
	openBraces := strings.Count(js, "{")
	closeBraces := strings.Count(js, "}")
	if openBraces > closeBraces {
		js += strings.Repeat("}", openBraces-closeBraces)
	}

	openBrackets := strings.Count(js, "[")
	closeBrackets := strings.Count(js, "]")
	if openBrackets > closeBrackets {
		js += strings.Repeat("]", openBrackets-closeBrackets)
	}

	// 4. Fix unescaped newlines inside JSON string values
	var sb strings.Builder
	inString := false
	inEscape := false
	for i := 0; i < len(js); i++ {
		c := js[i]
		if inEscape {
			sb.WriteByte(c)
			inEscape = false
			continue
		}
		if c == '\\' {
			sb.WriteByte(c)
			inEscape = true
			continue
		}
		if c == '"' {
			inString = !inString
			sb.WriteByte(c)
			continue
		}
		if inString && c == '\n' {
			sb.WriteString(`\n`)
		} else if inString && c == '\t' {
			sb.WriteString(`\t`)
		} else if inString && c == '\r' {
			sb.WriteString(`\r`)
		} else {
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func (r *ToolRegistry) GetAvailableTools(allowlist []string) []Tool {
	var allTools []Tool
	for name, t := range r.tools {
		if len(allowlist) == 0 {
			allTools = append(allTools, t.Definition())
		} else {
			allowed := false
			for _, allowedName := range allowlist {
				if name == allowedName || strings.HasPrefix(name, "mcp__") {
					allowed = true
					break
				}
			}
			if allowed {
				allTools = append(allTools, t.Definition())
			}
		}
	}

	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Function.Name < allTools[j].Function.Name
	})

	return allTools
}

func (r *ToolRegistry) GetAllExecutors() map[string]ToolExecutor {
	res := make(map[string]ToolExecutor, len(r.tools))
	for k, v := range r.tools {
		res[k] = v
	}
	return res
}
