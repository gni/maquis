package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"bidouille/pkg/ui/style"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
)

// HandleSlashCommand processes slash commands from the REPL.
// It returns (handled, quit).
func HandleSlashCommand(
	line string,
	cfg *config.Config,
	configPath string,
	httpClient *http.Client,
	messages *[]db.Message,
	allowedTools []string,
	theme *ui.UITheme,
	w io.Writer,
	currentSessionID *string,
) (bool, bool) {
	if !strings.HasPrefix(line, "/") && line != "?" {
		return false, false
	}

	parts := strings.Fields(line)
	cmdName := parts[0]

	switch cmdName {
	case "/exit", "/quit":
		return true, true
	case "/help", "?":
		ui.RenderHelp(w, *theme)
		return true, false
	case "/config":
		if len(parts) > 1 {
			if parts[1] == "show" {
				ui.RenderConfig(w, cfg, *theme)
				return true, false
			}

			startIndex := 1
			if parts[1] == "set" {
				if len(parts) < 4 {
					fmt.Fprintln(w, "Usage: /config [set] <key> <value>")
					return true, false
				}
				startIndex = 2
			}

			if len(parts) <= startIndex {
				fmt.Fprintln(w, "Usage: /config <key> <value>")
				return true, false
			}

			key := parts[startIndex]
			val := strings.Join(parts[startIndex+1:], " ")

			switch key {
			case "endpoint", "url":
				cfg.Endpoint = val
			case "model":
				cfg.Model = val
			case "temperature", "temp":
				t, err := strconv.ParseFloat(val, 64)
				if err != nil {
					fmt.Fprintf(w, "Invalid temperature value: %v\n", err)
					return true, false
				}
				cfg.Temperature = t
			case "auto_approve", "yes", "yolo":
				cfg.AutoApprove = val == "true" || val == "yes" || val == "1"
				cfg.YoloMode = cfg.AutoApprove
			case "show_thinking", "thinking":
				cfg.ShowThinking = val == "true" || val == "yes" || val == "1"
			case "reasoning_effort", "reasoning":
				cfg.ReasoningEffort = val
			case "before_tool_hook", "before_hook":
				cfg.BeforeToolHook = val
			case "after_tool_hook", "after_hook":
				cfg.AfterToolHook = val
			case "collapse_results", "collapse":
				cfg.CollapseResults = val == "true" || val == "yes" || val == "1"
			case "show_tokens", "tokens":
				cfg.ShowTokens = val == "true" || val == "yes" || val == "1"
			case "theme":
				cfg.Theme = val
				*theme = ui.GetTheme(val)
			case "cert_file", "cert":
				cfg.CertFile = val
			case "key_file", "key":
				cfg.KeyFile = val
			case "ca_file", "ca":
				cfg.CAFile = val
			case "skip_verify", "skip":
				cfg.SkipVerify = val == "true" || val == "yes" || val == "1"
			default:
				fmt.Fprintf(w, "Unknown config key: %s\n", key)
				return true, false
			}

			_ = config.SaveConfig(configPath, cfg)
			fmt.Fprintf(w, "Config updated. Saved to %s\n", configPath)
		} else {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			var input io.Reader = os.Stdin
			var output io.Writer = os.Stdout
			if err == nil {
				defer tty.Close()
				input = tty
				output = tty
			}

			newConfig, err := ui.RunInteractiveConfig(cfg, *theme, input, output)
			if err == nil && newConfig != nil {
				cfg = newConfig
				*theme = ui.GetTheme(cfg.Theme)
				_ = config.SaveConfig(configPath, cfg)
				fmt.Fprintf(w, "Configuration updated and saved to %s\n", configPath)
			} else {
				fmt.Fprintln(w, "Interactive config cancelled.")
			}
		}
		return true, false
	case "/skills":
		if len(parts) > 1 && parts[1] == "load" {
			if len(parts) < 3 {
				fmt.Fprintln(w, "Usage: /skills load <skill-name>")
				return true, false
			}
			name := parts[2]
			found := false
			for _, s := range ActiveSkills {
				if s.Name == name {
					*messages = append(*messages, db.Message{
						Role:    "system",
						Content: fmt.Sprintf("Loaded reference skill '%s':\n\n%s", s.Name, s.Content),
					})
					_ = db.SaveMessage(*currentSessionID, (*messages)[len(*messages)-1])
					fmt.Fprintf(w, "Loaded skill '%s' into the conversation context.\n", name)
					found = true
					break
				}
			}

			if !found {
				fmt.Fprintf(w, "Error: Skill '%s' not found.\n", name)
			}
		} else {
			RenderSkills(w, ActiveSkills, *theme)
		}
		return true, false
	case "/rewind":
		*messages = []db.Message{
			{Role: "system", Content: GetSystemPrompt(cfg)},
		}
		_ = db.ClearSession(*currentSessionID)
		_ = db.SaveMessage(*currentSessionID, (*messages)[0])
		fmt.Fprintln(w, "Conversation history cleared.")
		return true, false
	case "/session":
		if len(parts) > 1 {
			sub := parts[1]
			switch sub {
			case "list":
				sessions, err := db.GetSessions()
				if err != nil || len(sessions) == 0 {
					fmt.Fprintln(w, "No past sessions found.")
					return true, false
				}
				fmt.Fprintln(w, "Past Sessions:")
				for _, s := range sessions {
					activeMarker := ""
					if s.SessionID == *currentSessionID {
						activeMarker = " (active)"
					}
					previewText := s.Preview
					if len(previewText) > 40 {
						previewText = previewText[:40] + "..."
					}
					fmt.Fprintf(w, "  - %s [%s] (%d messages) - %s%s\n", s.SessionID, s.Timestamp[:16], s.MsgCount, previewText, activeMarker)
				}
			case "new":
				*currentSessionID = db.NewUUID()
				*messages = []db.Message{
					{Role: "system", Content: GetSystemPrompt(cfg)},
				}
				fmt.Fprintln(w, "Started a new conversation session.")
			case "branch":
				if len(parts) < 3 {
					fmt.Fprintln(w, "Usage: /session branch <new_session_id>")
					return true, false
				}
				branchID := parts[2]
				existing, err := db.LoadMessages(branchID)
				if err == nil && len(existing) > 0 {
					fmt.Fprintf(w, "Error: Session '%s' already exists.\n", branchID)
					return true, false
				}

				_ = db.ClearSession(branchID)
				for _, msg := range *messages {
					_ = db.SaveMessage(branchID, msg)
				}
				*currentSessionID = branchID
				fmt.Fprintf(w, "Successfully branched session into '%s'. Active session is now '%s'.\n", branchID, branchID)
			case "load":
				if len(parts) > 2 {
					selected := parts[2]
					dbHistory, err := db.LoadMessages(selected)
					if err == nil && len(dbHistory) > 0 {
						*currentSessionID = selected
						*messages = dbHistory
						fmt.Fprintf(w, "Loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
						printSessionHistory(w, *messages, *theme, cfg)
					} else {
						fmt.Fprintf(w, "Error: Session '%s' not found or empty.\n", selected)
					}
					return true, false
				}

				tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
				var input io.Reader = os.Stdin
				var output io.Writer = os.Stdout
				if err == nil {
					defer tty.Close()
					input = tty
					output = tty
				}

				selected, startNew, err := ui.RunSessionExplorer(*theme, input, output)
				if err == nil {
					if startNew {
						*currentSessionID = db.NewUUID()
						*messages = []db.Message{
							{Role: "system", Content: GetSystemPrompt(cfg)},
						}
						fmt.Fprintln(w, "Started a new conversation session.")
					} else if selected != "" {
						dbHistory, err := db.LoadMessages(selected)
						if err == nil && len(dbHistory) > 0 {
							*currentSessionID = selected
							*messages = dbHistory
							fmt.Fprintf(w, "Loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
							printSessionHistory(w, *messages, *theme, cfg)
						}
					}
				} else {
					fmt.Fprintln(w, "Session Explorer cancelled.")
				}
			default:
				fmt.Fprintln(w, "Usage: /session [list | new | load | branch <new_session_id>]")
			}
		} else {
			fmt.Fprintln(w, "Usage: /session [list | new | load | branch <new_session_id>]")
		}
		return true, false
	case "/multiline", "/paste":
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		var input io.Reader = os.Stdin
		var output io.Writer = os.Stdout
		if err == nil {
			defer tty.Close()
			input = tty
			output = tty
		}

		multilinePrompt, err := ui.RunMultilineEditor(input, output)
		if err == nil && strings.TrimSpace(multilinePrompt) != "" {
			RunAgentLoop(w, cfg, configPath, httpClient, messages, multilinePrompt, allowedTools, *theme, false, *currentSessionID)
		} else {
			fmt.Fprintln(w, "Multiline input cancelled.")
		}
		return true, false
	case "/goal":
		if len(parts) < 2 {
			fmt.Fprintln(w, "Usage: /goal <task description>")
			return true, false
		}
		task := strings.TrimPrefix(line, "/goal ")
		RunAgentLoop(w, cfg, configPath, httpClient, messages, task, allowedTools, *theme, false, *currentSessionID)
		return true, false
	case "/schedule":
		if len(parts) < 3 {
			fmt.Fprintln(w, "Usage: /schedule \"<duration/cron>\" <task description>")
			return true, false
		}
		rest := strings.TrimPrefix(line, "/schedule ")
		var schedStr string
		if strings.HasPrefix(rest, "\"") {
			endIdx := strings.Index(rest[1:], "\"")
			if endIdx != -1 {
				schedStr = rest[1 : endIdx+1]
				task := strings.TrimSpace(rest[endIdx+2:])
				fmt.Fprintf(w, "Scheduled task [ %s ] to run in [ %s ] (Simulation)\n", task, schedStr)
			} else {
				fmt.Fprintln(w, "Error: Unmatched quote in schedule expression.")
			}
		} else {
			partsSched := strings.SplitN(rest, " ", 2)
			if len(partsSched) == 2 {
				fmt.Fprintf(w, "Scheduled task [ %s ] to run in [ %s ] (Simulation)\n", partsSched[1], partsSched[0])
			}
		}
		return true, false
	case "/mcp":
		mcpStatuses := GetMCPServersStatus()
		if len(cfg.MCPServers) == 0 {
			fmt.Fprintln(w, "No MCP servers configured.")
			return true, false
		}

		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("MCP Server Connections:"))
		for name, serverCfg := range cfg.MCPServers {
			status, active := mcpStatuses[name]
			if !active {
				if err, failed := McpStartErrors[name]; failed {
					status = fmt.Sprintf("Failed to Start (%v)", err)
				} else {
					status = fmt.Sprintf("Not Connected (Configured URL: %s)", serverCfg.URL)
				}
			}
			fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
		}
		fmt.Fprintln(w)

		mcpTools := GetMCPTools()
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Available MCP Tools:"))
		if len(mcpTools) == 0 {
			fmt.Fprintln(w, "  (No tools registered)")
		} else {
			for _, t := range mcpTools {
				fmt.Fprintf(w, "  - %s: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(t.Function.Name),
					t.Function.Description,
				)
			}
		}
		return true, false
	default:
		fmt.Fprintf(w, "Unknown slash command: %s. Type /help or ? for commands list.\n", cmdName)
		return true, false
	}
}

func printSessionHistory(w io.Writer, messages []db.Message, theme ui.UITheme, cfg *config.Config) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	for _, msg := range messages {
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "user" {
			fmt.Fprintln(w, borderStyle.Render("─── Prompt ───────────────────────────────────────────────"))
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)
			fmt.Fprintln(w, borderStyle.Render("──────────────────────────────────────────────────────────"))
		} else if msg.Role == "assistant" {
			if msg.ReasoningContent != "" && cfg.ShowThinking {
				dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintln(w, dimStyle.Render(msg.ReasoningContent))
				fmt.Fprintln(w)

				iconStyle := style.NewStyle().Foreground(theme.Success)
				labelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintf(w, "%s %s\n\n", iconStyle.Render("✔"), labelStyle.Render("Thought"))
			}

			if msg.Content != "" {
				fmt.Fprintln(w, msg.Content)
				fmt.Fprintln(w)
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					var path string
					var argsMap map[string]interface{}
					if json.Unmarshal([]byte(tc.Function.Arguments), &argsMap) == nil {
						if p, ok := argsMap["path"].(string); ok {
							path = p
						} else if c, ok := argsMap["command"].(string); ok {
							path = c
						}
					}
					if path != "" {
						fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s %s", tc.Function.Name, path)))
					} else {
						fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s", tc.Function.Name)))
					}
				}
			}
		} else if msg.Role == "tool" {
			ui.RenderToolOutput(w, msg.Content, false, cfg.CollapseResults, theme)
		}
	}
}
