package tool

import (
	"encoding/json"
	"fmt"
)

type loadSkillTool struct{}

func NewLoadSkillTool() ToolExecutor {
	return &loadSkillTool{}
}

func (t *loadSkillTool) Name() string { return "load_skill" }

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
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	for _, s := range ctx.GetActiveSkills() {
		if s.Name == args.Name {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}
	return "", fmt.Errorf("skill '%s' not found", args.Name)
}
