package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"maquis/pkg/agent"
	"maquis/pkg/db"
)

// RunExtension checks for a custom executable script matching the slash command and runs it securely.
func RunExtension(
	a *agent.Agent,
	cmdName string,
	args []string,
	messages *[]db.Message,
	w io.Writer,
) (bool, error) {
	if a.Config.DisableLocalPlugins {
		return true, fmt.Errorf("local extensions are disabled for this workspace (run with trust to enable)")
	}

	// Strip leading slash (e.g. "/stats" -> "stats")
	name := strings.TrimPrefix(cmdName, "/")
	if name == "" {
		return false, nil
	}

	// Validate name characters to prevent directory traversal
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false, nil
		}
	}

	// Define extension directories
	var dirs []string
	dirs = append(dirs, filepath.Join(a.GetWorkspaceRoot(), "extensions"))

	var extPath string
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			// Match base filename (e.g. "stats" matches "stats.py", "stats.sh", "stats")
			base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if strings.ToLower(base) == strings.ToLower(name) {
				pluginPath := filepath.Join(dir, entry.Name())
				info, err := os.Stat(pluginPath)
				if err == nil && info.Mode()&0111 != 0 {
					extPath = pluginPath
					break
				}
			}
		}
		if extPath != "" {
			break
		}
	}

	if extPath == "" {
		return false, nil // No matching extension found
	}

	// Enforce 15-second execution timeout
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, extPath, args...)

	// Pass current conversation messages as a JSON array to stdin
	if messages != nil {
		if jsonData, err := json.Marshal(*messages); err == nil {
			cmd.Stdin = bytes.NewReader(jsonData)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctxTimeout.Err() == context.DeadlineExceeded {
		return true, fmt.Errorf("extension timed out after 15 seconds")
	}

	// Output stdout directly to the terminal writer
	if stdout.Len() > 0 {
		_, _ = io.Copy(w, &stdout)
	}

	if err != nil {
		return true, fmt.Errorf("%w (details: %s)", err, strings.TrimSpace(stderr.String()))
	}

	return true, nil
}
