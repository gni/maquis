package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

type loadSkillTool struct{}

func NewLoadSkillTool() ToolExecutor {
	return &loadSkillTool{}
}

func (t *loadSkillTool) Name() string { return "load_skill" }

func (t *loadSkillTool) PromptSnippet() string {
	return "Load detailed reference skill instructions"
}

func (t *loadSkillTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "load_skill",
			Description: "Retrieve the detailed instructions, tools, or references for a specific skill from the available skills list.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"name": {
						Type:        "string",
						Description: "The name of the skill to load (e.g. 'agent-isolation').",
					},
				},
				Required: []string{"name"},
			},
		},
	}
}

func (t *loadSkillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	name := strings.TrimSpace(arguments)
	if strings.HasPrefix(name, "\"") && strings.HasSuffix(name, "\"") && len(name) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(name), &unquoted); err == nil {
			name = unquoted
		}
	} else if strings.HasPrefix(name, "{") {
		var args struct {
			Name      string `json:"name"`
			Skill     string `json:"skill"`
			SkillName string `json:"skill_name"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Name != "" {
				name = args.Name
			} else if args.Skill != "" {
				name = args.Skill
			} else if args.SkillName != "" {
				name = args.SkillName
			}
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("missing required argument: skill name")
	}

	for _, s := range ctx.GetActiveSkills() {
		if strings.EqualFold(s.Name, name) {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}

	// Not found, reload from disk and check again
	reloaded := ctx.ReloadSkills()
	for _, s := range reloaded {
		if strings.EqualFold(s.Name, name) {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}

	return "", fmt.Errorf("skill '%s' not found", name)
}
