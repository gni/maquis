package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"bidouille/pkg/ui/style"

	"bidouille/pkg/config"
	"github.com/alecthomas/chroma/v2/quick"
)

func PrintBanner(w io.Writer, cfg *config.Config) {
	theme := GetTheme(cfg.Theme)

	iconStyle := style.NewStyle().
		Foreground(theme.Secondary).
		MarginLeft(2)

	infoStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		MarginLeft(4)

	icon := `   /\___/\
  (  o.o  )
  (  =^=  )
   \_____/`

	info := fmt.Sprintf("Bidouille CLI v1.0.0\nEndpoint: %s\nModel:    %s", cfg.Endpoint, cfg.Model)

	joined := style.JoinHorizontal(
		style.Center,
		iconStyle.Render(icon),
		infoStyle.Render(info),
	)

	fmt.Fprintln(w, joined)
	fmt.Fprintln(w)
}

func RenderHelp(w io.Writer, theme UITheme) {
	headerStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Underline(true)

	cmdStyle := style.NewStyle().
		Foreground(theme.Secondary).
		Bold(true)

	descStyle := style.NewStyle().
		Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("Slash Commands Reference:"))
	fmt.Fprintln(w)

	commands := [][]string{
		{"/goal <task>", "Set a goal and trigger the Bidouille reasoning loop"},
		{"/schedule \"<cron/duration>\" <task>", "Schedule a task execution simulation"},
		{"/config", "Open interactive settings editor"},
		{"/config show", "Display current settings summary"},
		{"/config set <key> <value>", "Modify settings dynamically"},
		{"/session <list/new/load>", "Manage persistent chat sessions interactively"},
		{"/multiline, /paste", "Open interactive multiline editor"},
		{"/skills", "List all available reference skills"},
		{"/skills load <name>", "Explicitly load a reference skill into the context"},
		{"/mcp", "List all configured MCP servers and active tool schemas"},
		{"/rewind", "Clear conversation history"},
		{"/help, /commands, ?", "Display this help menu"},
		{"/exit, /quit", "Exit the Bidouille CLI application"},
	}

	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render(cmd[0]), descStyle.Render(cmd[1]))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, headerStyle.Render("Keyboard Shortcuts:"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("Ctrl+O"), descStyle.Render("Toggle tool results collapsing (COLLAPSED vs. FULL)"))
	fmt.Fprintln(w)

	fmt.Fprintln(w, headerStyle.Render("Local Command Execution:"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("!<command>"), descStyle.Render("Execute any local shell command (e.g. !git diff)"))
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("<direct_command>"), descStyle.Render("Execute common utilities directly (if enabled)"))
	fmt.Fprintln(w, "  Supported direct utilities: ls, pwd, git, cat, cd")
	fmt.Fprintln(w)
}

func RenderConfig(w io.Writer, cfg *config.Config, theme UITheme) {
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	keyStyle := style.NewStyle().
		Foreground(theme.Secondary)

	valStyle := style.NewStyle().
		Foreground(theme.Text)

	approveVal := "Disabled (Interactive Mode)"
	if cfg.IsAutoApprove() {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("Enabled (Auto-Approve)")
	}

	directVal := "Disabled"
	if cfg.DirectCommands {
		directVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("Enabled")
	}

	configStr := fmt.Sprintf(
		"%s\n\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %.2f\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %d tokens\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n\n"+
			"Tip: Change any setting via: /config <key> <value> (e.g. /config yes true)",
		titleStyle.Render("BIDOUILLE RUNTIME SETTINGS"),
		keyStyle.Render("Endpoint:"), valStyle.Render(cfg.Endpoint),
		keyStyle.Render("Model:"), valStyle.Render(cfg.Model),
		keyStyle.Render("Temperature:"), cfg.Temperature,
		keyStyle.Render("Auto-Approve:"), approveVal,
		keyStyle.Render("Show Thinking:"), cfg.ShowThinking,
		keyStyle.Render("Collapse Results:"), cfg.CollapseResults,
		keyStyle.Render("Show Tokens:"), cfg.ShowTokens,
		keyStyle.Render("Context Limit:"), cfg.ContextWindowLimit,
		keyStyle.Render("Direct Commands:"), directVal,
		keyStyle.Render("Client Cert:"), valStyle.Render(cfg.CertFile),
		keyStyle.Render("Client Key:"), valStyle.Render(cfg.KeyFile),
		keyStyle.Render("Skip SSL Verify:"), valStyle.Render(fmt.Sprintf("%v", cfg.SkipVerify)),
	)

	fmt.Fprintln(w, borderStyle.Render(configStr))
}

func formatToolArguments(toolName string, argsJSON string, theme UITheme) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return "arguments: " + argsJSON
	}

	keyStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	valStyle := style.NewStyle().Foreground(theme.Text)

	var sb strings.Builder

	// We separate "simple" arguments (e.g., path, limit) from "block" arguments (e.g., content, command, edits)
	var simpleLines []string
	var blockLines []string

	// Order keys: put 'path' first if it exists
	keys := make([]string, 0, len(m))
	if _, ok := m["path"]; ok {
		keys = append(keys, "path")
	}
	for k := range m {
		if k != "path" {
			keys = append(keys, k)
		}
	}

	for _, k := range keys {
		v := m[k]
		switch k {
		case "content", "command":
			strVal, ok := v.(string)
			if !ok {
				strVal = fmt.Sprintf("%v", v)
			}

			lang := "plaintext"
			if k == "command" {
				lang = "bash"
			} else if pathVal, ok := m["path"].(string); ok {
				ext := filepath.Ext(pathVal)
				if len(ext) > 1 {
					lang = ext[1:]
				}
			}

			var codeBuf bytes.Buffer
			err := quick.Highlight(&codeBuf, strVal, lang, "terminal16", "friendly")
			var highlightedStr string
			if err == nil {
				highlightedStr = codeBuf.String()
			} else {
				highlightedStr = strVal
			}

			// Pretty format multi-line text
			var blockSb strings.Builder
			blockSb.WriteString(fmt.Sprintf("%s:\n", keyStyle.Render(k)))
			lines := strings.Split(highlightedStr, "\n")
			for i, line := range lines {
				// Don't print empty trailing line if it's the last one
				if line == "" && i == len(lines)-1 {
					continue
				}
				blockSb.WriteString(fmt.Sprintf("%s\n", line))
			}
			blockLines = append(blockLines, strings.TrimSuffix(blockSb.String(), "\n"))

		case "edits", "updates":
			// Parse edits/updates list
			edits, ok := v.([]interface{})
			if !ok {
				continue
			}
			var blockSb strings.Builder
			blockSb.WriteString(fmt.Sprintf("%s:\n", keyStyle.Render(k)))
			for i, eVal := range edits {
				eMap, ok := eVal.(map[string]interface{})
				if !ok {
					continue
				}
				oldText, _ := eMap["oldText"].(string)
				newText, _ := eMap["newText"].(string)

				blockSb.WriteString(fmt.Sprintf("edit block %d:\n", i+1))
				if oldText != "" {
					blockSb.WriteString(style.NewStyle().Foreground(theme.Error).Render("- [old text]:\n"))
					for _, line := range strings.Split(oldText, "\n") {
						blockSb.WriteString(fmt.Sprintf("%s\n", style.NewStyle().Foreground(theme.Error).Render(line)))
					}
				}
				if newText != "" {
					blockSb.WriteString(style.NewStyle().Foreground(theme.Success).Render("+ [new text]:\n"))
					for _, line := range strings.Split(newText, "\n") {
						blockSb.WriteString(fmt.Sprintf("%s\n", style.NewStyle().Foreground(theme.Success).Render(line)))
					}
				}
			}
			blockLines = append(blockLines, strings.TrimSuffix(blockSb.String(), "\n"))

		default:
			// Normal inline key value
			valStr := fmt.Sprintf("%v", v)
			simpleLines = append(simpleLines, fmt.Sprintf("%s: %s", keyStyle.Render(k), valStyle.Render(valStr)))
		}
	}

	if len(simpleLines) > 0 {
		sb.WriteString(strings.Join(simpleLines, "\n"))
	}
	if len(blockLines) > 0 {
		if len(simpleLines) > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.Join(blockLines, "\n\n"))
	}

	return sb.String()
}

func RenderToolCall(w io.Writer, toolName string, arguments string, theme UITheme) {
	titleStyle := style.NewStyle().
		Foreground(theme.Secondary).
		Bold(true)

	formattedArgs := formatToolArguments(toolName, arguments, theme)

	fmt.Fprintln(w, titleStyle.Render(fmt.Sprintf("tool call: %s", toolName)))
	fmt.Fprint(w, formattedArgs)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme UITheme) {
	fmt.Fprintln(w) // Print a leading blank line to separate from the tool call or previous elements
	borderColor := theme.Success
	title := "tool output"
	if isError {
		borderColor = theme.Error
		title = "tool error"
	}

	titleStyle := style.NewStyle().
		Foreground(borderColor).
		Bold(true)

	bodyStyle := style.NewStyle().
		Foreground(theme.Text)

	body := output
	if strings.HasPrefix(output, "Successfully edited ") || strings.Contains(output, "● Edit") || strings.Contains(output, "edit:") {
		collapse = false
	}
	if !isError && len(strings.Split(output, "\n")) > 4 {
		if collapse {
			body = fmt.Sprintf("success (returned %d lines, %d bytes) [/toggle or Ctrl+O to expand]", len(strings.Split(output, "\n")), len(output))
		} else {
			body = output + "\n[/toggle or Ctrl+O to collapse]"
		}
	} else if collapse {
		lines := strings.Split(output, "\n")
		if len(lines) > 10 {
			truncated := lines[:10]
			body = strings.Join(truncated, "\n") + fmt.Sprintf("\n... (%d more lines) [/toggle or Ctrl+O to expand]", len(lines)-10)
		}
	} else {
		lines := strings.Split(output, "\n")
		if len(lines) > 10 {
			body = output + "\n[/toggle or Ctrl+O to collapse]"
		}
	}

	fmt.Fprintln(w, titleStyle.Render(title+":"))
	for _, line := range strings.Split(body, "\n") {
		var renderedLine string
		if strings.Contains(line, "\x1b[") {
			renderedLine = line
		} else {
			renderedLine = bodyStyle.Render(line)
		}
		fmt.Fprintln(w, renderedLine)
	}
}

func RenderMCPStartupErrors(w io.Writer, startErrors map[string]error, theme UITheme) {
	if len(startErrors) == 0 {
		return
	}

	titleStyle := style.NewStyle().
		Foreground(theme.Error).
		Bold(true)

	msgStyle := style.NewStyle().
		Foreground(theme.Text)

	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Error).
		Padding(0, 1).
		Margin(1, 0)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Failed to start MCP server(s):") + "\n")
	for name, err := range startErrors {
		sb.WriteString(fmt.Sprintf("  - %s: %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), msgStyle.Render(err.Error())))
	}

	fmt.Fprint(w, borderStyle.Render(strings.TrimSuffix(sb.String(), "\n")))
	fmt.Fprintln(w)
}
