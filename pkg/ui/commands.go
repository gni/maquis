package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"maquis/pkg/ui/style"

	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
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
	mam *agent.MultiAgentManager,
	rlInput io.Reader,
) (bool, bool) {
	trimmed := strings.TrimSpace(line)
	isHelp := trimmed == "help" || trimmed == "h" || trimmed == "?" || trimmed == "/help" || trimmed == "/commands" || trimmed == "/h" || trimmed == "/?"
	if !strings.HasPrefix(trimmed, "/") && !isHelp {
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
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)

		// Clear screen and redraw everything up to history
		fmt.Fprint(w, "\x1b[H\x1b[J")
		if len(a.McpStartErrors) > 0 {
			RenderMCPStartupErrors(w, a.McpStartErrors, *theme)
		}
		PrintBanner(w, a)
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
	case "/help", "/h", "/commands", "?", "/?", "help", "h":
		RenderHelp(w, *theme)
		return true, false
	case "/config", "/set":
		if len(parts) > 1 {
			if cmdName == "/config" && parts[1] == "show" {
				RenderConfig(w, a.Config, *theme)
				return true, false
			}

			startIndex := 1
			if cmdName == "/config" && parts[1] == "set" {
				if len(parts) < 4 {
					fmt.Fprintln(w, "usage: /config [set] <key> <value>")
					return true, false
				}
				startIndex = 2
			} else if cmdName == "/set" {
				if len(parts) < 3 {
					fmt.Fprintln(w, "usage: /set <key> <value>")
					return true, false
				}
				startIndex = 1
			}

			if len(parts) <= startIndex {
				if cmdName == "/set" {
					fmt.Fprintln(w, "usage: /set <key> <value>")
				} else {
					fmt.Fprintln(w, "usage: /config <key> <value>")
				}
				return true, false
			}

			key := parts[startIndex]
			val := strings.Join(parts[startIndex+1:], " ")

			switch key {
			case "endpoint", "url":
				a.Config.Endpoint = val
				a.Config.UpdateActiveProvider()
			case "model":
				a.Config.Model = val
				a.Config.UpdateActiveProvider()
			case "api_key", "key":
				a.Config.ApiKey = val
				a.Config.UpdateActiveProvider()
			case "temperature", "temp":
				t, err := strconv.ParseFloat(val, 64)
				if err != nil {
					fmt.Fprintf(w, "Invalid temperature value: %v\n", err)
					return true, false
				}
				a.Config.Temperature = t
			case "auto_approve", "yes", "yolo":
				a.Config.AutoApprove = val == "true" || val == "yes" || val == "1"
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
				*theme = GetConfiguredTheme(a.Config)
			case "syntax_theme", "syntax":
				a.Config.SyntaxTheme = val
				*theme = GetConfiguredTheme(a.Config)
			case "context_window_limit", "context_limit", "context":
				l, err := strconv.Atoi(val)
				if err != nil || l <= 0 {
					fmt.Fprintf(w, "Invalid context window limit value: %v\n", err)
					return true, false
				}
				a.Config.ContextWindowLimit = l
			case "max_completion_tokens", "max_tokens", "output_tokens":
				tokens, err := strconv.Atoi(val)
				if err != nil || tokens <= 0 {
					fmt.Fprintf(w, "Invalid max completion tokens value: %v\n", err)
					return true, false
				}
				a.Config.MaxCompletionTokens = tokens
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
			case "key_file", "client_key":
				a.Config.KeyFile = val
			case "ca_file", "ca":
				a.Config.CAFile = val
			case "skip_verify", "skip":
				a.Config.SkipVerify = val == "true" || val == "yes" || val == "1"
			case "stream_writes", "stream_write", "stream":
				a.Config.StreamWrites = val == "true" || val == "yes" || val == "1"
			default:
				fmt.Fprintf(w, "unknown config key: %s\n", key)
				return true, false
			}

			_ = config.SaveConfig(a.ConfigPath, a.Config)
			fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
			DrawStatusBar(w, *theme)
		} else {
			var input io.Reader = rlInput
			if input == nil {
				input = os.Stdin
				if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
					defer tty.Close()
					input = tty
				}
			}
			var output io.Writer = os.Stdout
			if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
				defer tty.Close()
				output = tty
			}

			ShutdownStatusBar(os.Stderr)
			newConfig, errInteractive := RunInteractiveConfig(a.Config, *theme, input, output)
			InitStatusBar(os.Stderr)
			if errInteractive == nil && newConfig != nil {
				a.Config = newConfig
				*theme = GetConfiguredTheme(a.Config)
				_ = config.SaveConfig(a.ConfigPath, a.Config)
			}
			
			// Clear screen and redraw everything up to history
			fmt.Fprint(w, "\x1b[H\x1b[J")
			if len(a.McpStartErrors) > 0 {
				RenderMCPStartupErrors(w, a.McpStartErrors, *theme)
			}
			PrintBanner(w, a)
			
			if errInteractive != nil {
				fmt.Fprintln(w, "interactive config cancelled.")
			} else if newConfig != nil {
				fmt.Fprintf(w, "configuration updated and saved to %s\n", a.ConfigPath)
			}
			
			PrintSessionHistory(w, *messages, *theme, a.Config)
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
			DrawStatusBar(os.Stderr, *theme)
		}
		return true, false
	case "/provider", "/providers":
		HandleProviderCommand(a, parts, messages, *theme, w, rlInput)
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
		} else if len(parts) > 1 && parts[1] == "reload" {
			_ = a.ReloadSkills()
			fmt.Fprintln(w, "successfully reloaded all skills from disk.")
		} else {
			agent.RenderSkills(w, a.ActiveSkills, *theme)
		}
		return true, false
	case "/rewind":
		getUI().LastStatsText = ""
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
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
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
				getUI().LastStatsText = ""
				*currentSessionID = db.NewUUID()
				*messages = []db.Message{
					{Role: "system", Content: a.GetSystemPrompt()},
				}
				fmt.Fprintln(w, "started a new conversation session.")
				pTok, cTok := calcHistoryTokens()
				UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
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
						getUI().LastStatsText = ""
						*currentSessionID = selected
						*messages = dbHistory
						fmt.Fprintf(w, "loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
						PrintSessionHistory(w, *messages, *theme, a.Config)

						pTok, cTok := calcHistoryTokens()
						UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
						DrawStatusBar(os.Stderr, *theme)
					} else {
						fmt.Fprintf(w, "error: session '%s' not found or empty.\n", selected)
					}
					return true, false
				}

				var input io.Reader = rlInput
				if input == nil {
					input = os.Stdin
					if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
						defer tty.Close()
						input = tty
					}
				}
				var output io.Writer = os.Stdout
				if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
					defer tty.Close()
					output = tty
				}

				ShutdownStatusBar(os.Stderr)
				selected, startNew, err := RunSessionExplorer(*theme, input, output)
				InitStatusBar(os.Stderr)
				
				// Clear screen and redraw everything up to history
				fmt.Fprint(w, "\x1b[H\x1b[J")
				if len(a.McpStartErrors) > 0 {
					RenderMCPStartupErrors(w, a.McpStartErrors, *theme)
				}
				PrintBanner(w, a)
				
				if err == nil {
					getUI().LastStatsText = ""
					if startNew {
						*currentSessionID = db.NewUUID()
						*messages = []db.Message{
							{Role: "system", Content: a.GetSystemPrompt()},
						}
						fmt.Fprintln(w, "started a new conversation session.")
					} else if selected != "" {
						dbHistory, err := db.LoadMessages(selected)
						if err == nil && len(dbHistory) > 0 {
							*currentSessionID = selected
							*messages = dbHistory
							fmt.Fprintf(w, "loaded session %s (%d messages).\n", *currentSessionID, len(*messages))
						}
					}
				} else {
					fmt.Fprintln(w, "session explorer cancelled.")
				}
				
				PrintSessionHistory(w, *messages, *theme, a.Config)
				pTok, cTok := calcHistoryTokens()
				UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
				DrawStatusBar(os.Stderr, *theme)
			case "clear":
				err := db.ClearHistory()
				if err != nil {
					fmt.Fprintf(w, "error clearing sessions: %v\n", err)
					return true, false
				}
				getUI().LastStatsText = ""
				*currentSessionID = db.NewUUID()
				*messages = []db.Message{
					{Role: "system", Content: a.GetSystemPrompt()},
				}
				fmt.Fprintln(w, "all conversation sessions deleted from disk.")
				pTok, cTok := calcHistoryTokens()
				UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
				DrawStatusBar(os.Stderr, *theme)
			default:
				fmt.Fprintf(w, "active session: %s\n", *currentSessionID)
				fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id> | clear]")
			}
		} else {
			fmt.Fprintf(w, "active session: %s\n", *currentSessionID)
			fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id> | clear]")
		}
		return true, false
	case "/mcp", "/mcps":
		HandleMCPCommand(a, parts, messages, *theme, w, rlInput)
		return true, false
	case "/agent":
		if len(parts) < 2 {
			var input io.Reader = rlInput
			if input == nil {
				input = os.Stdin
				if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
					defer tty.Close()
					input = tty
				}
			}
			var output io.Writer = os.Stdout
			if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
				defer tty.Close()
				output = tty
			}

			ShutdownStatusBar(os.Stderr)
			_ = RunInteractiveAgentManager(mam, *theme, input, output)
			InitStatusBar(os.Stderr)

			// Clear screen and redraw everything up to history
			fmt.Fprint(w, "\x1b[H\x1b[J")
			if len(a.McpStartErrors) > 0 {
				RenderMCPStartupErrors(w, a.McpStartErrors, *theme)
			}
			PrintBanner(w, a)
			PrintSessionHistory(w, *messages, *theme, a.Config)
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
			DrawStatusBar(os.Stderr, *theme)
			return true, false
		}
		sub := parts[1]
		switch sub {
		case "list":
			agentsList := mam.ListAgents()
			if len(agentsList) == 0 {
				fmt.Fprintln(w, "no active multi-agents spawned. (default chat is routed to base agent)")
				return true, false
			}
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("active agents:"))
			activeName := mam.ActiveAgentName()

			for _, name := range agentsList {
				marker := " "
				if name == activeName {
					marker = style.NewStyle().Foreground(theme.Success).Render("➔")
				}
				parentName := mam.GetParentName(name)
				parentStr := ""
				if parentName != "" {
					parentStr = fmt.Sprintf(" (subagent of %s)", parentName)
				}

				fmt.Fprintf(w, "  %s %-15s : %s\n", marker, style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), parentStr)
			}
			if activeName == "" {
				fmt.Fprintf(w, "  %s %-15s : (currently chatting with default base agent)\n", style.NewStyle().Foreground(theme.Success).Render("➔"), "base")
			}
		case "join":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /agent join <name> (use 'base' to return to base agent)")
				return true, false
			}
			target := parts[2]
			if target == "base" || target == "main" {
				mam.JoinAgent("base")
				fmt.Fprintln(w, "switched back to default base agent.")
				return true, false
			}
			joined := mam.JoinAgent(target)
			if joined {
				fmt.Fprintf(w, "switched chat focus to agent '%s'.\n", target)
			} else {
				fmt.Fprintf(w, "error: agent '%s' not found.\n", target)
			}
		case "spawn":
			if len(parts) < 4 {
				fmt.Fprintln(w, "usage: /agent spawn <name> <prompt> [parent_name] [skill_name]")
				return true, false
			}
			name := parts[2]
			
			// Prompt can be multi-word, so we extract it carefully
			promptStart := fieldStartIndex(line, 3)
			if promptStart == -1 {
				fmt.Fprintln(w, "error: failed to parse prompt")
				return true, false
			}
			prompt := line[promptStart:]
			parentName := ""
			skillName := ""

			// Loop to dynamically extract trailing arguments that match parent agents or active skills
			for {
				lastSpace := strings.LastIndex(prompt, " ")
				if lastSpace == -1 {
					break
				}
				lastWord := strings.TrimSpace(prompt[lastSpace+1:])
				
				// Check if it is a known skill name
				isSkill := false
				for _, s := range a.ActiveSkills {
					if s.Name == lastWord {
						isSkill = true
						break
					}
				}

				if isSkill && skillName == "" {
					skillName = lastWord
					prompt = strings.TrimSpace(prompt[:lastSpace])
					continue
				}

				// Check if it is an existing agent name
				if mam.HasAgent(lastWord) && parentName == "" {
					parentName = lastWord
					prompt = strings.TrimSpace(prompt[:lastSpace])
					continue
				}

				// If it matches neither, or both are already resolved, stop checking
				break
			}

			err := mam.SpawnAgent(name, prompt, parentName, skillName)
			if err != nil {
				fmt.Fprintf(w, "error spawning agent: %v\n", err)
			} else {
				var info string
				if parentName != "" && skillName != "" {
					info = fmt.Sprintf("subagent '%s' under parent '%s' with dedicated skill '%s'", name, parentName, skillName)
				} else if parentName != "" {
					info = fmt.Sprintf("subagent '%s' under parent '%s'", name, parentName)
				} else if skillName != "" {
					info = fmt.Sprintf("independent agent '%s' with dedicated skill '%s'", name, skillName)
				} else {
					info = fmt.Sprintf("independent agent '%s'", name)
				}
				fmt.Fprintf(w, "successfully spawned %s.\n", info)
			}
		case "skill":
			if len(parts) < 4 {
				fmt.Fprintln(w, "usage: /agent skill [list <agent_name> | load <agent_name> <skill_name> | clear <agent_name>]")
				return true, false
			}
			op := parts[2]
			agentName := parts[3]

			switch op {
			case "list":
				skills, err := mam.ListAgentSkills(agentName)
				if err != nil {
					fmt.Fprintf(w, "error: %v\n", err)
					return true, false
				}
				if len(skills) == 0 {
					fmt.Fprintf(w, "agent '%s' has no loaded skills.\n", agentName)
					return true, false
				}
				fmt.Fprintf(w, "skills loaded for agent '%s':\n", agentName)
				for _, s := range skills {
					fmt.Fprintf(w, "  - %s: %s\n", style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(s.Name), s.Description)
				}
			case "load":
				if len(parts) < 5 {
					fmt.Fprintln(w, "usage: /agent skill load <agent_name> <skill_name>")
					return true, false
				}
				skillName := parts[4]
				err := mam.LoadAgentSkill(agentName, skillName)
				if err != nil {
					fmt.Fprintf(w, "error: %v\n", err)
				} else {
					fmt.Fprintf(w, "successfully loaded skill '%s' into agent '%s'.\n", skillName, agentName)
				}
			case "clear":
				err := mam.ClearAgentSkills(agentName)
				if err != nil {
					fmt.Fprintf(w, "error: %v\n", err)
				} else {
					fmt.Fprintf(w, "cleared all skills for agent '%s'.\n", agentName)
				}
			default:
				fmt.Fprintln(w, "unknown skill operation. usage: /agent skill [list <agent_name> | load <agent_name> <skill_name> | clear <agent_name>]")
			}
		case "kill":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /agent kill <name>")
				return true, false
			}
			target := parts[2]
			err := mam.KillAgent(target)
			if err != nil {
				fmt.Fprintf(w, "error killing agent: %v\n", err)
			} else {
				fmt.Fprintf(w, "agent '%s' terminated.\n", target)
			}
		default:
			fmt.Fprintln(w, "unknown agent subcommand. usage: /agent [list | join <name> | spawn <name> <prompt> [parent_name] [skill_name] | kill <name> | skill [list/load/clear] ...]")
		}
		return true, false
	case "/reload":
		err := a.ReloadPlugins()
		if err != nil {
			fmt.Fprintf(w, "error reloading plugins: %v\n", err)
		} else {
			fmt.Fprintln(w, "reload ok")
		}
		return true, false
	case "/plugins":
		executors := a.Registry.GetAllExecutors()
		var pluginNames []string
		for name := range executors {
			if strings.HasPrefix(name, "plugin__") {
				pluginNames = append(pluginNames, name)
			}
		}
		sort.Strings(pluginNames)
		if len(pluginNames) == 0 {
			fmt.Fprintln(w, "no custom plugins registered.")
			return true, false
		}
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("custom plugin tools:"))
		for _, name := range pluginNames {
			exec := executors[name]
			fmt.Fprintf(w, "  - %-25s : %s\n",
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
				exec.Definition().Function.Description,
			)
		}
		return true, false
	case "/extensions":
		var dirs []string
		home, err := os.UserHomeDir()
		if err == nil {
			dirs = append(dirs, filepath.Join(home, ".maquis", "extensions"))
		}
		dirs = append(dirs, filepath.Join(a.GetWorkspaceRoot(), "extensions"))

		type extInfo struct {
			name string
			path string
			loc  string
		}
		var exts []extInfo
		seen := make(map[string]bool)

		for _, dir := range dirs {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			loc := "project"
			if strings.Contains(dir, ".maquis") {
				loc = "global"
			}

			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				// Check executable permission
				path := filepath.Join(dir, entry.Name())
				info, err := os.Stat(path)
				if err == nil && info.Mode()&0111 != 0 {
					base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
					cmdName := "/" + strings.ToLower(base)
					if !seen[cmdName] {
						seen[cmdName] = true
						exts = append(exts, extInfo{
							name: cmdName,
							path: path,
							loc:  loc,
						})
					}
				}
			}
		}

		sort.Slice(exts, func(i, j int) bool {
			return exts[i].name < exts[j].name
		})

		if len(exts) == 0 {
			fmt.Fprintln(w, "no custom slash command extensions found.")
			return true, false
		}

		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("custom slash command extensions:"))
		for _, ext := range exts {
			fmt.Fprintf(w, "  - %-20s : %s (%s)\n",
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(ext.name),
				ext.path,
				ext.loc,
			)
		}
		return true, false
	default:
		handledExt, errExt := RunExtension(a, cmdName, parts[1:], messages, w)
		if handledExt {
			if errExt != nil {
				fmt.Fprintf(w, "extension error: %v\n", errExt)
			}
			return true, false
		}
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

// fieldStartIndex returns the start index of the fieldIndex-th word (0-based) in s.
func fieldStartIndex(s string, fieldIndex int) int {
	inWord := false
	wordCount := 0
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inWord = false
		} else {
			if !inWord {
				if wordCount == fieldIndex {
					return i
				}
				wordCount++
				inWord = true
			}
		}
	}
	return -1
}
