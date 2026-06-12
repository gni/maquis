package tool

import (
	"encoding/json"
	"fmt"
)

type taskStatusTool struct{}

func NewTaskStatusTool() ToolExecutor {
	return &taskStatusTool{}
}

func (t *taskStatusTool) Name() string { return "task_status" }

func (t *taskStatusTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "task_status",
			Description: "Retrieve the execution status and buffered stdout/stderr output of a background task.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"task_id": {
						Type:        "string",
						Description: "The ID of the background task (e.g. 'task_1').",
					},
				},
				Required: []string{"task_id"},
			},
		},
	}
}

func (t *taskStatusTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		TaskId string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	status, output, err := ctx.GetTaskStatus(args.TaskId)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s is currently: %s\n\nOutput:\n%s", args.TaskId, status, output), nil
}

type taskKillTool struct{}

func NewTaskKillTool() ToolExecutor {
	return &taskKillTool{}
}

func (t *taskKillTool) Name() string { return "task_kill" }

func (t *taskKillTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "task_kill",
			Description: "Terminate a running background task.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"task_id": {
						Type:        "string",
						Description: "The ID of the task to terminate.",
					},
				},
				Required: []string{"task_id"},
			},
		},
	}
}

func (t *taskKillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		TaskId string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	err := ctx.KillTask(args.TaskId)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s successfully terminated.", args.TaskId), nil
}
