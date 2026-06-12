package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"bidouille/pkg/ui/style"

	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
)

// HandleSlashCommand processes slash commands from the REPL.
// It returns (handled, quit).
func (a *Agent) HandleSlashCommand(
	line string,
	messages *[]db.Message,
	allowedTools []string,
	theme *ui.UITheme,
	w io.Writer,
	currentSessionID *string,
	rlHistory term.History,
) (bool, bool) {
	if !strings.HasPrefix(line, "/") && line != "?" {
		return false, false
	}

	parts := strings.Fields(line)
	cmdName := parts[0]

	calcHistoryTokens := func() (int, int) {
		return a.GetGlobalTokens(*messages, allowedTools)
	}

	switch cmdName {
	case "/exit", "/quit":
		return true, true
	case "/toggle", "/collapse", "/expand":
		if cmdName == "/collapse" {
			a.Config.CollapseResults = true
		} else if cmdName == "/expand" {
			a.Config.CollapseResults = false
		} else {
			a.Config.CollapseResults = !a.Config.CollapseResults
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		pTok, cTok := calcHistoryTokens()
		ui.SetCollapseStatus(a.Config.CollapseResults)
		ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)

		// Clear screen and redraw everything up to history
		fmt.Fprint(w, "\x1b[H\x1b[2J")
		ui.PrintBanner(w, a.Config)
		printSessionHistory(w, *messages, *theme, a.Config)
		return true, false
	case "/task":
		if len(parts) < 2 {
			fmt.Fprintln(w, "Usage: /task [list | view <id> | stream <id> | kill <id>]")
			return true, false
		}
		sub := parts[1]
		switch sub {
		case "list":
			tasks := a.ListTasks()
			if len(tasks) == 0 {
				fmt.Fprintln(w, "No background tasks registered.")
				return true, false
			}
			fmt.Fprintln(w, "Background Tasks:")
			for _, t := range tasks {
				fmt.Fprintf(w, "  - %s: %s (Duration: %v, Output Size: %d bytes) - `%s`\n", 
					t.ID, t.Status, t.Duration.Round(time.Millisecond), t.BytesOut, t.Command)
			}
		case "view":
			if len(parts) < 3 {
				fmt.Fprintln(w, "Usage: /task view <id>")
				return true, false
			}
			id := parts[2]
			status, output, err := a.GetTaskStatus(id)
			if err != nil {
				fmt.Fprintf(w, "Error: %v\n", err)
				return true, false
			}
			fmt.Fprintf(w, "Task %s Status: %s\nOutput:\n%s\n", id, status, output)
		case "stream":
			if len(parts) < 3 {
				fmt.Fprintln(w, "Usage: /task stream <id>")
				return true, false
			}
			id := parts[2]
			a.ToggleStreaming(id, w)
		case "kill":
			if len(parts) < 3 {
				fmt.Fprintln(w, "Usage: /task kill <id>")
				return true, false
			}
			id := parts[2]
			err := a.KillTask(id)
			if err != nil {
				fmt.Fprintf(w, "Error: %v\n", err)
			} else {
				fmt.Fprintf(w, "Task %s successfully terminated.\n", id)
			}
		default:
			fmt.Fprintln(w, "Unknown task subcommand. Usage: /task [list | view <id> | stream <id> | kill <id>]")
		}
		return true, false
	case "/help", "/commands", "?":
		ui.RenderHelp(w, *theme)
		return true, false
	case "/config":
		if len(parts) > 1 {
			if parts[1] == "show" {
				ui.RenderConfig(w, a.Config, *theme)
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
				a.Config.Endpoint = val
			case "model":
				a.Config.Model = val
			case "temperature", "temp":
				t, err := strconv.ParseFloat(val, 64)
				if err != nil {
					fmt.Fprintf(w, "Invalid temperature value: %v\n", err)
					return true, false
				}
				a.Config.Temperature = t
			case "auto_approve", "yes", "yolo":
				a.Config.AutoApprove = val == "true" || val == "yes" || val == "1"
				a.Config.YoloMode = a.Config.AutoApprove
			case "show_thinking", "thinking":
				a.Config.ShowThinking = val == "true" || val == "yes" || val == "1"
			case "reasoning_effort", "reasoning":
				a.Config.ReasoningEffort = val
			case "before_tool_hook", "before_hook":
				a.Config.BeforeToolHook = val
			case "after_tool_hook", "after_hook":
				a.Config.AfterToolHook = val
			case "collapse_results", "collapse":
				a.Config.CollapseResults = val == "true" || val == "yes" || val == "1"
			case "show_tokens", "tokens":
				a.Config.ShowTokens = val == "true" || val == "yes" || val == "1"
			case "theme":
				a.Config.Theme = val
				*theme = ui.GetTheme(val)
			case "context_window_limit", "context_limit", "context":
				l, err := strconv.Atoi(val)
				if err != nil || l <= 0 {
					fmt.Fprintf(w, "Invalid context window limit value: %v\n", err)
					return true, false
				}
				a.Config.ContextWindowLimit = l
			case "max_reasoning_steps", "max_steps", "steps":
				steps, err := strconv.Atoi(val)
				if err != nil || steps <= 0 {
					fmt.Fprintf(w, "Invalid max reasoning steps value: %v\n", err)
					return true, false
				}
				a.Config.MaxReasoningSteps = steps
			case "direct_commands", "direct":
				a.Config.DirectCommands = val == "true" || val == "yes" || val == "1"
			case "cert_file", "cert":
				a.Config.CertFile = val
			case "key_file", "key":
				a.Config.KeyFile = val
			case "ca_file", "ca":
				a.Config.CAFile = val
			case "skip_verify", "skip":
				a.Config.SkipVerify = val == "true" || val == "yes" || val == "1"
			default:
				fmt.Fprintf(w, "Unknown config key: %s\n", key)
				return true, false
			}

			_ = config.SaveConfig(a.ConfigPath, a.Config)
			fmt.Fprintf(w, "Config updated. Saved to %s\n", a.ConfigPath)
			pTok, cTok := calcHistoryTokens()
			ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
			ui.DrawStatusBar(w, *theme)
		} else {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			var input io.Reader = os.Stdin
			var output io.Writer = os.Stdout
			if err == nil {
				defer tty.Close()
				input = tty
				output = tty
			}

			ui.ShutdownStatusBar(os.Stderr)
			newConfig, err := ui.RunInteractiveConfig(a.Config, *theme, input, output)
			ui.InitStatusBar(os.Stderr)
			if err == nil && newConfig != nil {
				a.Config = newConfig
				*theme = ui.GetTheme(a.Config.Theme)
				_ = config.SaveConfig(a.ConfigPath, a.Config)
				fmt.Fprintf(w, "Configuration updated and saved to %s\n", a.ConfigPath)
			} else {
				fmt.Fprintln(w, "Interactive config cancelled.")
			}
			pTok, cTok := calcHistoryTokens()
			ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
			ui.DrawStatusBar(os.Stderr, *theme)
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
			for _, s := range a.ActiveSkills {
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
			RenderSkills(w, a.ActiveSkills, *theme)
		}
		return true, false
	case "/rewind":
		*messages = []db.Message{
			{Role: "system", Content: a.GetSystemPrompt()},
		}
		_ = db.ClearSession(*currentSessionID)
		_ = db.SaveMessage(*currentSessionID, (*messages)[0])
		if ch, ok := rlHistory.(*customHistory); ok {
			ch.entries = nil
		}
		// Clear terminal screen and scrollback
		fmt.Fprint(w, "\x1b[H\x1b[2J")
		fmt.Fprintln(w, "Conversation history cleared.")
		pTok, cTok := calcHistoryTokens()
		ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
		ui.DrawStatusBar(os.Stderr, *theme)
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
					{Role: "system", Content: a.GetSystemPrompt()},
				}
				fmt.Fprintln(w, "Started a new conversation session.")
				pTok, cTok := calcHistoryTokens()
				ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
				ui.DrawStatusBar(os.Stderr, *theme)
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
				ui.DrawStatusBar(os.Stderr, *theme)
			case "load":
				if len(parts) > 2 {
					selected := parts[2]
					dbHistory, err := db.LoadMessages(selected)
					if err == nil && len(dbHistory) > 0 {
						*currentSessionID = selected
						*messages = dbHistory
						fmt.Fprintf(w, "Loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
						printSessionHistory(w, *messages, *theme, a.Config)

						pTok, cTok := calcHistoryTokens()
						ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
						ui.DrawStatusBar(os.Stderr, *theme)
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

				ui.ShutdownStatusBar(os.Stderr)
				selected, startNew, err := ui.RunSessionExplorer(*theme, input, output)
				ui.InitStatusBar(os.Stderr)
				if err == nil {
					if startNew {
						*currentSessionID = db.NewUUID()
						*messages = []db.Message{
							{Role: "system", Content: a.GetSystemPrompt()},
						}
						fmt.Fprintln(w, "Started a new conversation session.")
						pTok, cTok := calcHistoryTokens()
						ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
						ui.DrawStatusBar(os.Stderr, *theme)
					} else if selected != "" {
						dbHistory, err := db.LoadMessages(selected)
						if err == nil && len(dbHistory) > 0 {
							*currentSessionID = selected
							*messages = dbHistory
							fmt.Fprintf(w, "Loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
							printSessionHistory(w, *messages, *theme, a.Config)

							pTok, cTok := calcHistoryTokens()
							ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0)
							ui.DrawStatusBar(os.Stderr, *theme)
						}
					}
				} else {
					fmt.Fprintln(w, "Session Explorer cancelled.")
				}
				ui.DrawStatusBar(os.Stderr, *theme)
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

		ui.ShutdownStatusBar(os.Stderr)
		multilinePrompt, err := ui.RunMultilineEditor(input, output)
		ui.InitStatusBar(os.Stderr)
		ui.DrawStatusBar(os.Stderr, *theme)
		if err == nil && strings.TrimSpace(multilinePrompt) != "" {
			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), multilinePrompt)
			a.RunAgentLoop(w, messages, multilinePrompt, allowedTools, *theme, false, *currentSessionID)
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
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), task)
		a.RunAgentLoop(w, messages, task, allowedTools, *theme, false, *currentSessionID)
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
		mcpStatuses := a.GetMCPServersStatus()
		if len(a.Config.MCPServers) == 0 {
			fmt.Fprintln(w, "No MCP servers configured.")
			return true, false
		}

		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("MCP Server Connections:"))
		for name, serverCfg := range a.Config.MCPServers {
			status, active := mcpStatuses[name]
			if !active {
				if err, failed := a.McpStartErrors[name]; failed {
					status = fmt.Sprintf("Failed to Start (%v)", err)
				} else {
					status = fmt.Sprintf("Not Connected (Configured URL: %s)", serverCfg.URL)
				}
			}
			fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
		}
		fmt.Fprintln(w)

		mcpTools := a.GetMCPTools()
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

	var lastRole string
	for _, msg := range messages {
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "user" {
			if strings.HasPrefix(msg.Content, "[User manually executed local shell command: `") {
				firstTick := strings.Index(msg.Content, "`")
				if firstTick != -1 {
					rest := msg.Content[firstTick+1:]
					secondTick := strings.Index(rest, "`")
					if secondTick != -1 {
						cmdStr := rest[:secondTick]
						output := ""
						newLineIdx := strings.Index(rest, "\n")
						if newLineIdx != -1 {
							output = rest[newLineIdx+1:]
						}
						if lastRole != "" {
							fmt.Fprintln(w)
						}
						fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> !"), cmdStr)
						if output != "" {
							fmt.Fprint(w, output)
						}
						lastRole = "user"
						continue
					}
				}
			}

			if lastRole != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, borderStyle.Render("─── Prompt ───────────────────────────────────────────────"))
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)
		} else if msg.Role == "assistant" {
			if lastRole == "tool" {
				fmt.Fprintln(w)
			}
			hasPrintedAnything := false
			if msg.ReasoningContent != "" && cfg.ShowThinking {
				dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintln(w, dimStyle.Render(msg.ReasoningContent))
				fmt.Fprintln(w)

				iconStyle := style.NewStyle().Foreground(theme.Success)
				labelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintf(w, "%s %s\n", iconStyle.Render("✔"), labelStyle.Render("Thought"))
				hasPrintedAnything = true
			}

			if msg.Content != "" {
				if hasPrintedAnything {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, msg.Content)
				hasPrintedAnything = true
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if hasPrintedAnything {
						fmt.Fprintln(w)
					}
					var path string
					var argsMap map[string]interface{}
					if json.Unmarshal([]byte(tc.Function.Arguments), &argsMap) == nil {
						if p, ok := argsMap["path"].(string); ok {
							path = p
						} else if c, ok := argsMap["command"].(string); ok {
							path = c
						}
					}
					isPathTool := tc.Function.Name == "read" || tc.Function.Name == "write" || tc.Function.Name == "edit" || tc.Function.Name == "ls" || tc.Function.Name == "grep" || tc.Function.Name == "find"
					var symbol string
					if tc.Function.Name == "write" {
						symbol = style.NewStyle().Foreground(theme.Success).Bold(true).Render("◆")
					} else {
						symbol = style.NewStyle().Foreground(theme.Success).Bold(true).Render("▸")
					}
					var title string
					if isPathTool && path != "" {
						title = ui.FormatToolTitle(symbol, tc.Function.Name, path, theme)
					} else {
						title = ui.FormatToolTitle(symbol, tc.Function.Name, "", theme)
					}
					fmt.Fprintln(w, title)
					hasPrintedAnything = true
				}
			}
		} else if msg.Role == "tool" {
			var toolName string
			var argsJSON string
			for i := len(messages) - 1; i >= 0; i-- {
				m := messages[i]
				if m.Role == "assistant" {
					for _, tc := range m.ToolCalls {
						if tc.ID == msg.ToolCallID {
							toolName = tc.Function.Name
							argsJSON = tc.Function.Arguments
							break
						}
					}
				}
				if toolName != "" {
					break
				}
			}
			if toolName == "" {
				toolName = msg.Name
			}
			ui.RenderToolOutput(w, msg.Content, false, cfg.CollapseResults, theme, toolName, argsJSON, -1)
		}
		lastRole = msg.Role
	}
}
