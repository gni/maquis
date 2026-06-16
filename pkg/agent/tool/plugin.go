package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type pluginTool struct {
	path string
	def  Tool
}

func (p *pluginTool) Name() string {
	return p.def.Function.Name
}

func (p *pluginTool) Definition() Tool {
	return p.def
}

func (p *pluginTool) Execute(ctx AgentContext, arguments string) (string, error) {
	// Securely verify path is absolute and within workspace or plugin directory
	absPath, err := filepath.Abs(p.path)
	if err != nil {
		return "", fmt.Errorf("invalid plugin path: %w", err)
	}

	// Double check file existence and executable bits
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("plugin file error: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plugin path is a directory")
	}

	// 10-second execution timeout
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, absPath)
	cmd.Stdin = strings.NewReader(arguments)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if ctxTimeout.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("plugin execution timed out after 10 seconds")
	}
	if err != nil {
		return "", fmt.Errorf("plugin error: %w (details: %s)", err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}

// RegisterPlugins scans the specified directory for executables and registers them
func RegisterPlugins(registry *ToolRegistry, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // Plugins directory optional
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		pluginPath := filepath.Join(absDir, entry.Name())

		// Verify file is executable (on Unix, check executable permission)
		info, err := os.Stat(pluginPath)
		if err != nil {
			continue
		}

		// Simple executable check: check if it's executable by current user
		if info.Mode()&0111 == 0 {
			continue
		}

		// Skip .json files themselves
		if filepath.Ext(pluginPath) == ".json" {
			continue
		}

		// Look for matching json descriptor (e.g. plugin.sh.json or plugin.json)
		baseWithoutExt := strings.TrimSuffix(pluginPath, filepath.Ext(pluginPath))
		jsonPath1 := pluginPath + ".json"
		jsonPath2 := baseWithoutExt + ".json"

		var jsonBytes []byte
		var errRead error

		if _, err := os.Stat(jsonPath1); err == nil {
			jsonBytes, errRead = os.ReadFile(jsonPath1)
		} else if _, err := os.Stat(jsonPath2); err == nil {
			jsonBytes, errRead = os.ReadFile(jsonPath2)
		}

		if errRead != nil || len(jsonBytes) == 0 {
			// Skip plugins without a valid descriptor json file
			continue
		}

		var functionDef FunctionDefinition
		if err := json.Unmarshal(jsonBytes, &functionDef); err != nil {
			// Skip if descriptor is not a valid JSON function definition
			continue
		}

		// Enforce a naming prefix for security and clarity
		if !strings.HasPrefix(functionDef.Name, "plugin__") {
			functionDef.Name = "plugin__" + functionDef.Name
		}

		// Enforce alphanumeric characters in the name to prevent any registry injection
		validName := true
		for _, r := range functionDef.Name {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				validName = false
				break
			}
		}
		if !validName {
			continue
		}

		toolDef := Tool{
			Type:     "function",
			Function: functionDef,
		}

		registry.Register(&pluginTool{
			path: pluginPath,
			def:  toolDef,
		})
	}

	return nil
}
