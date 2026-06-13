package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"bidouille/pkg/ui/style"

	"golang.org/x/term"

	"bidouille/pkg/agent"
	"bidouille/pkg/config"
	"bidouille/pkg/db"
)

// HandleSlashCommand processes slash commands from the REPL.
// It returns (handled, quit).
func HandleSlashCommand(
	a *agent.Agent,
	line string,
	messages *[]db.Message,
	allowedTools []string,
	theme *UITheme,
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
		SetCollapseStatus(a.Config.CollapseResults)
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))

		// Clear screen and redraw everything up to history
		fmt.Fprint(w, "\x1b[H\x1b[2J")
		if len(a.McpStartErrors) > 0 {
			RenderMCPStartupErrors(w, a.McpStartErrors, *theme)
		}
		PrintBanner(w, a.Config)
		PrintSessionHistory(w, *messages, *theme, a.Config)
		return true, false
	case "/task":
		if len(parts) < 2 {
			fmt.Fprintln(w, "usage: /task [list | view <id> | stream <id> | kill <id>]")
			return true, false
		}
		sub := parts[1]
		switch sub {
		case "list":
			tasks := a.ListTasks()
			if len(tasks) == 0 {
				fmt.Fprintln(w, "no background tasks registered.")
				return true, false
			}
			fmt.Fprintln(w, "background tasks:")
			for _, t := range tasks {
				fmt.Fprintf(w, "  - %s: %s (duration: %v, output size: %d bytes) - `%s`\n", 
					t.ID, t.Status, t.Duration.Round(time.Millisecond), t.BytesOut, t.Command)
			}
		case "view":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /task view <id>")
				return true, false
			}
			id := parts[2]
			status, output, err := a.GetTaskStatus(id)
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
				return true, false
			}
			fmt.Fprintf(w, "task %s status: %s\noutput:\n%s\n", id, status, output)
		case "stream":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /task stream <id>")
				return true, false
			}
			id := parts[2]
			a.ToggleStreaming(id, w)
		case "kill":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /task kill <id>")
				return true, false
			}
			id := parts[2]
			err := a.KillTask(id)
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
			} else {
				fmt.Fprintf(w, "task %s successfully terminated.\n", id)
			}
		default:
			fmt.Fprintln(w, "unknown task subcommand. usage: /task [list | view <id> | stream <id> | kill <id>]")
		}
		return true, false
	case "/help", "/commands", "?":
		RenderHelp(w, *theme)
		return true, false
	case "/config":
		if len(parts) > 1 {
			if parts[1] == "show" {
				RenderConfig(w, a.Config, *theme)
				return true, false
			}

			startIndex := 1
			if parts[1] == "set" {
				if len(parts) < 4 {
					fmt.Fprintln(w, "usage: /config [set] <key> <value>")
					return true, false
				}
				startIndex = 2
			}

			if len(parts) <= startIndex {
				fmt.Fprintln(w, "usage: /config <key> <value>")
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
				*theme = GetTheme(val)
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
				fmt.Fprintf(w, "unknown config key: %s\n", key)
				return true, false
			}

			_ = config.SaveConfig(a.ConfigPath, a.Config)
			fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
			DrawStatusBar(w, *theme)
		} else {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			var input io.Reader = os.Stdin
			var output io.Writer = os.Stdout
			if err == nil {
				defer tty.Close()
				input = tty
				output = tty
			}

			ShutdownStatusBar(os.Stderr)
			newConfig, err := RunInteractiveConfig(a.Config, *theme, input, output)
			InitStatusBar(os.Stderr)
			if err == nil && newConfig != nil {
				a.Config = newConfig
				*theme = GetTheme(a.Config.Theme)
				_ = config.SaveConfig(a.ConfigPath, a.Config)
				fmt.Fprintf(w, "configuration updated and saved to %s\n", a.ConfigPath)
			} else {
				fmt.Fprintln(w, "interactive config cancelled.")
			}
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
			DrawStatusBar(os.Stderr, *theme)
		}
		return true, false
	case "/skills":
		if len(parts) > 1 && parts[1] == "load" {
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /skills load <skill-name>")
				return true, false
			}
			name := parts[2]
			found := false
			for _, s := range a.ActiveSkills {
				if s.Name == name {
					*messages = append(*messages, db.Message{
						Role:    "system",
						Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", s.Name, s.Content),
					})
					_ = db.SaveMessage(*currentSessionID, (*messages)[len(*messages)-1])
					fmt.Fprintf(w, "loaded skill '%s' into the conversation context.\n", name)
					found = true
					break
				}
			}

			if !found {
				fmt.Fprintf(w, "error: skill '%s' not found.\n", name)
			}
		} else {
			agent.RenderSkills(w, a.ActiveSkills, *theme)
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
		fmt.Fprintln(w, "conversation history cleared.")
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
		DrawStatusBar(w, *theme)
		return true, false
	case "/session":
		if len(parts) > 1 {
			sub := parts[1]
			switch sub {
			case "list":
				sessions, err := db.GetSessions()
				if err != nil || len(sessions) == 0 {
					fmt.Fprintln(w, "no past sessions found.")
					return true, false
				}
				fmt.Fprintln(w, "past sessions:")
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
				fmt.Fprintln(w, "started a new conversation session.")
				pTok, cTok := calcHistoryTokens()
				UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
				DrawStatusBar(os.Stderr, *theme)
			case "branch":
				if len(parts) < 3 {
					fmt.Fprintln(w, "usage: /session branch <new_session_id>")
					return true, false
				}
				branchID := parts[2]
				existing, err := db.LoadMessages(branchID)
				if err == nil && len(existing) > 0 {
					fmt.Fprintf(w, "error: session '%s' already exists.\n", branchID)
					return true, false
				}

				_ = db.ClearSession(branchID)
				for _, msg := range *messages {
					_ = db.SaveMessage(branchID, msg)
				}
				*currentSessionID = branchID
				fmt.Fprintf(w, "successfully branched session into '%s'. active session is now '%s'.\n", branchID, branchID)
				DrawStatusBar(os.Stderr, *theme)
			case "load":
				if len(parts) > 2 {
					selected := parts[2]
					dbHistory, err := db.LoadMessages(selected)
					if err == nil && len(dbHistory) > 0 {
						*currentSessionID = selected
						*messages = dbHistory
						fmt.Fprintf(w, "loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
						PrintSessionHistory(w, *messages, *theme, a.Config)

						pTok, cTok := calcHistoryTokens()
						UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
						DrawStatusBar(os.Stderr, *theme)
					} else {
						fmt.Fprintf(w, "error: session '%s' not found or empty.\n", selected)
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

				ShutdownStatusBar(os.Stderr)
				selected, startNew, err := RunSessionExplorer(*theme, input, output)
				InitStatusBar(os.Stderr)
				if err == nil {
					if startNew {
						*currentSessionID = db.NewUUID()
						*messages = []db.Message{
							{Role: "system", Content: a.GetSystemPrompt()},
						}
						fmt.Fprintln(w, "started a new conversation session.")
						pTok, cTok := calcHistoryTokens()
						UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
						DrawStatusBar(os.Stderr, *theme)
					} else if selected != "" {
						dbHistory, err := db.LoadMessages(selected)
						if err == nil && len(dbHistory) > 0 {
							*currentSessionID = selected
							*messages = dbHistory
							fmt.Fprintf(w, "loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
							PrintSessionHistory(w, *messages, *theme, a.Config)

							pTok, cTok := calcHistoryTokens()
							UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a))
							DrawStatusBar(os.Stderr, *theme)
						}
					}
				} else {
					fmt.Fprintln(w, "session explorer cancelled.")
				}
				DrawStatusBar(os.Stderr, *theme)
			default:
				fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id>]")
			}
		} else {
			fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id>]")
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

		ShutdownStatusBar(w)
		multilinePrompt, err := RunMultilineEditor(input, output)
		InitStatusBar(w)
		DrawStatusBar(w, *theme)
		if err == nil && strings.TrimSpace(multilinePrompt) != "" {
			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), multilinePrompt)
			a.RunAgentLoop(w, messages, multilinePrompt, allowedTools, *theme, false, *currentSessionID)
		} else {
			fmt.Fprintln(w, "multiline input cancelled.")
		}
		return true, false
	case "/goal":
		if len(parts) < 2 {
			fmt.Fprintln(w, "usage: /goal <task description>")
			return true, false
		}
		task := strings.TrimPrefix(line, "/goal ")
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), task)
		a.RunAgentLoop(w, messages, task, allowedTools, *theme, false, *currentSessionID)
		return true, false
	case "/schedule":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /schedule \"<duration/cron>\" <task description>")
			return true, false
		}
		rest := strings.TrimPrefix(line, "/schedule ")
		var schedStr string
		if strings.HasPrefix(rest, "\"") {
			endIdx := strings.Index(rest[1:], "\"")
			if endIdx != -1 {
				schedStr = rest[1 : endIdx+1]
				task := strings.TrimSpace(rest[endIdx+2:])
				fmt.Fprintf(w, "scheduled task [ %s ] to run in [ %s ] (simulation)\n", task, schedStr)
			} else {
				fmt.Fprintln(w, "error: unmatched quote in schedule expression.")
			}
		} else {
			partsSched := strings.SplitN(rest, " ", 2)
			if len(partsSched) == 2 {
				fmt.Fprintf(w, "scheduled task [ %s ] to run in [ %s ] (simulation)\n", partsSched[1], partsSched[0])
			}
		}
		return true, false
	case "/mcp":
		mcpStatuses := a.GetMCPServersStatus()
		if len(a.Config.MCPServers) == 0 {
			fmt.Fprintln(w, "no mcp servers configured.")
			return true, false
		}

		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("mcp server connections:"))
		for name, serverCfg := range a.Config.MCPServers {
			status, active := mcpStatuses[name]
			if !active {
				if err, failed := a.McpStartErrors[name]; failed {
					status = fmt.Sprintf("failed to start (%v)", err)
				} else {
					status = fmt.Sprintf("not connected (configured url: %s)", serverCfg.URL)
				}
			}
			fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
		}
		fmt.Fprintln(w)

		mcpTools := a.GetMCPTools()
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("available mcp tools:"))
		if len(mcpTools) == 0 {
			fmt.Fprintln(w, "  (no tools registered)")
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
		fmt.Fprintf(w, "unknown slash command: %s. type /help or ? for commands list.\n", cmdName)
		return true, false
	}
}

func getActiveTasks(a *agent.Agent) int {
	activeTasks := 0
	if a != nil {
		for _, t := range a.ListTasks() {
			if t.Status == "running" {
				activeTasks++
			}
		}
	}
	return activeTasks
}
