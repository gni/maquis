package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseTaskID(arguments string) (string, error) {
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			trimmed = unquoted
		}
	} else if strings.HasPrefix(trimmed, "{") {
		var args struct {
			TaskID    string `json:"task_id"`
			TaskIDAlt string `json:"taskId"`
			ID        string `json:"id"`
			Task      string `json:"task"`
		}
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		if args.TaskID != "" {
			trimmed = args.TaskID
		} else if args.TaskIDAlt != "" {
			trimmed = args.TaskIDAlt
		} else if args.ID != "" {
			trimmed = args.ID
		} else if args.Task != "" {
			trimmed = args.Task
		}
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", fmt.Errorf("missing required argument: task_id")
	}
	return trimmed, nil
}

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
	taskID, err := parseTaskID(arguments)
	if err != nil {
		return "", err
	}

	status, output, err := ctx.GetTaskStatus(taskID)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s is currently: %s\n\nOutput:\n%s", taskID, status, output), nil
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
	taskID, err := parseTaskID(arguments)
	if err != nil {
		return "", err
	}

	err = ctx.KillTask(taskID)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task %s successfully terminated.", taskID), nil
}
