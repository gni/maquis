package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"
	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

func AskForApproval(w io.Writer, theme UITheme) (bool, bool) {
	var input io.Reader = os.Stdin
	var output io.Writer = os.Stdout
	var fd int = int(os.Stdin.Fd())

	stateMu.Lock()
	hasActiveReader := activeInputReader != nil
	if hasActiveReader {
		input = activeInputReader
		output = w
	}
	stateMu.Unlock()

	stateMu.Lock()
	inApprovalPrompt = true
	stateMu.Unlock()
	defer func() {
		stateMu.Lock()
		inApprovalPrompt = false
		stateMu.Unlock()
	}()

	if !hasActiveReader {
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			input = tty
			output = tty
			fd = int(tty.Fd())
		}
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	fmt.Fprint(output, promptStyle.Render(" Approve tool execution? [y/N/a (always)]: "))

	isTerm := term.IsTerminal(fd)
	if isTerm && !hasActiveReader {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	buf := make([]byte, 1)
	n, err := input.Read(buf)
	if err != nil || n == 0 {
		if isTerm {
			fmt.Fprint(output, "\r\x1b[K")
		} else {
			fmt.Fprintln(output)
		}
		return false, false
	}

	char := buf[0]
	// Handle Ctrl+C (3) or Esc (27)
	if char == 3 || char == 27 {
		if isTerm {
			fmt.Fprint(output, "\r\x1b[K")
		} else {
			fmt.Fprintln(output, "rejected")
		}
		return false, false
	}

	if char == 'y' || char == 'Y' {
		if isTerm {
			fmt.Fprint(output, "\r\x1b[K")
		} else {
			fmt.Fprintln(output, "y")
		}
		return true, false
	} else if char == 'a' || char == 'A' {
		if isTerm {
			fmt.Fprint(output, "\r\x1b[K")
		} else {
			fmt.Fprintln(output, "always")
		}
		return true, true
	} else {
		if isTerm {
			fmt.Fprint(output, "\r\x1b[K")
		} else {
			if char == '\r' || char == '\n' {
				fmt.Fprintln(output, "n")
			} else {
				fmt.Fprintf(output, "%c\n", char)
			}
		}
		return false, false
	}
}

func RunInteractiveConfig(cfg *config.Config, theme UITheme, rlInput io.Reader, rlOutput io.Writer) (*config.Config, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return cfg, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, oldState)

	// Save screen state, switch to alternate screen, and hide cursor
	fmt.Fprint(rlOutput, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(rlOutput, "\x1b[?25h\x1b[?1049l")

	formatBool := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}

	// Create a copy of the config to allow cancellation if desired
	cloned := *cfg

	type settingItem struct {
		id          string
		name        string
		value       func() string
		description string
		options     []string
		isBool      bool
		onToggle    func()
		onEdit      func(newVal string) error
	}

	var items []*settingItem
	items = []*settingItem{
		{
			id:          "active_provider",
			name:        "active provider",
			value:       func() string {
				if cloned.ActiveProvider == "" {
					return "none (default)"
				}
				return cloned.ActiveProvider
			},
			description: "Currently active endpoint provider profile",
			onToggle: func() {
				var keys []string
				keys = append(keys, "")
				for k := range cloned.Providers {
					keys = append(keys, k)
				}
				sort.Strings(keys[1:])

				idx := 0
				for i, k := range keys {
					if cloned.ActiveProvider == k {
						idx = i
						break
					}
				}
				nextIdx := (idx + 1) % len(keys)
				cloned.ActiveProvider = keys[nextIdx]
				if cloned.ActiveProvider != "" {
					cloned.SyncActiveProvider()
				}
			},
		},
		{
			id:          "endpoint",
			name:        "endpoint url",
			value:       func() string { return cloned.Endpoint },
			description: "API endpoint URL for the LLM service",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				cloned.Endpoint = newVal
				cloned.UpdateActiveProvider()
				return nil
			},
		},
		{
			id:          "model",
			name:        "model name",
			value:       func() string { return cloned.Model },
			description: "Name of the LLM model to use",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				cloned.Model = newVal
				cloned.UpdateActiveProvider()
				return nil
			},
		},
		{
			id:          "temperature",
			name:        "temperature",
			value:       func() string { return fmt.Sprintf("%.2f", cloned.Temperature) },
			description: "Sampling temperature for response generation (0.0 to 2.0)",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				t, err := strconv.ParseFloat(newVal, 64)
				if err != nil || t < 0.0 || t > 2.0 {
					return fmt.Errorf("must be a number between 0.0 and 2.0")
				}
				cloned.Temperature = t
				return nil
			},
		},
		{
			id:          "theme",
			name:        "visual theme",
			value:       func() string { return cloned.Theme },
			description: "Visual aesthetic style of the terminal theme",
			options:     []string{"dark", "neon", "light", "gruvbox", "mono", "minimal", "plain"},
			onToggle: func() {
				themes := []string{"dark", "neon", "light", "gruvbox", "mono", "minimal", "plain"}
				idx := -1
				for i, t := range themes {
					if strings.ToLower(cloned.Theme) == t {
						idx = i
						break
					}
				}
				nextIdx := (idx + 1) % len(themes)
				cloned.Theme = themes[nextIdx]
				theme = GetConfiguredTheme(&cloned) // dynamically apply theme visually in real-time
			},
		},
		{
			id:          "syntax_theme",
			name:        "syntax highlight style",
			value:       func() string { return cloned.SyntaxTheme },
			description: "Chroma syntax theme (e.g. auto, nord, dracula, gruvbox, bw, monokai)",
			options:     []string{"auto", "nord", "dracula", "gruvbox", "bw", "monokai", "solarized-dark", "solarized-light"},
			onToggle: func() {
				styles := []string{"auto", "nord", "dracula", "gruvbox", "bw", "monokai", "solarized-dark", "solarized-light"}
				idx := -1
				for i, s := range styles {
					if strings.ToLower(cloned.SyntaxTheme) == s {
						idx = i
						break
					}
				}
				nextIdx := (idx + 1) % len(styles)
				cloned.SyntaxTheme = styles[nextIdx]
				theme = GetConfiguredTheme(&cloned) // dynamically apply theme visually in real-time
			},
		},
		{
			id:          "auto_approve",
			name:        "auto-approve",
			value:       func() string { return formatBool(cloned.AutoApprove) },
			description: "Automatically approve all tool execution prompts",
			isBool:      true,
			onToggle: func() {
				cloned.AutoApprove = !cloned.AutoApprove
			},
		},
		{
			id:          "show_thinking",
			name:        "show thinking",
			value:       func() string { return formatBool(cloned.ShowThinking) },
			description: "Stream the LLM thinking/reasoning process",
			isBool:      true,
			onToggle: func() {
				cloned.ShowThinking = !cloned.ShowThinking
			},
		},
		{
			id:          "collapse_results",
			name:        "collapse results",
			value:       func() string { return formatBool(cloned.CollapseResults) },
			description: "Collapse tool results output to keep history clean",
			isBool:      true,
			onToggle: func() {
				cloned.CollapseResults = !cloned.CollapseResults
			},
		},
		{
			id:          "show_tokens",
			name:        "show tokens",
			value:       func() string { return formatBool(cloned.ShowTokens) },
			description: "Display token usage metrics under each response",
			isBool:      true,
			onToggle: func() {
				cloned.ShowTokens = !cloned.ShowTokens
			},
		},
		{
			id:          "context_window_limit",
			name:        "context limit",
			value:       func() string { return fmt.Sprintf("%d", cloned.ContextWindowLimit) },
			description: "Maximum token context window limit before compression",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				l, err := strconv.Atoi(newVal)
				if err != nil || l <= 0 {
					return fmt.Errorf("must be a positive integer")
				}
				cloned.ContextWindowLimit = l
				return nil
			},
		},
		{
			id:          "max_reasoning_steps",
			name:        "max reasoning steps",
			value:       func() string { return fmt.Sprintf("%d", cloned.MaxReasoningSteps) },
			description: "Maximum number of sequential reasoning steps before termination",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				steps, err := strconv.Atoi(newVal)
				if err != nil || steps <= 0 {
					return fmt.Errorf("must be a positive integer")
				}
				cloned.MaxReasoningSteps = steps
				return nil
			},
		},
		{
			id:          "direct_commands",
			name:        "direct commands",
			value:       func() string { return formatBool(cloned.DirectCommands) },
			description: "Enable direct execution of local shell commands",
			isBool:      true,
			onToggle: func() {
				cloned.DirectCommands = !cloned.DirectCommands
			},
		},
		{
			id:          "cert_file",
			name:        "client cert",
			value:       func() string { return cloned.CertFile },
			description: "Path to the SSL client certificate file",
			onEdit: func(newVal string) error {
				cloned.CertFile = newVal
				return nil
			},
		},
		{
			id:          "key_file",
			name:        "client key",
			value:       func() string { return cloned.KeyFile },
			description: "Path to the SSL client private key file",
			onEdit: func(newVal string) error {
				cloned.KeyFile = newVal
				return nil
			},
		},
		{
			id:          "skip_verify",
			name:        "skip ssl verify",
			value:       func() string { return formatBool(cloned.SkipVerify) },
			description: "Skip SSL/TLS certificate verification",
			isBool:      true,
			onToggle: func() {
				cloned.SkipVerify = !cloned.SkipVerify
			},
		},
		{
			id:          "stream_writes",
			name:        "stream writes",
			value:       func() string { return formatBool(cloned.StreamWrites) },
			description: "Stream file contents in real-time as they are written",
			isBool:      true,
			onToggle: func() {
				cloned.StreamWrites = !cloned.StreamWrites
			},
		},
	}

	searchQuery := ""
	selectedIdx := 0

	for {
		// Filter items based on search query
		var filtered []*settingItem
		for _, item := range items {
			valStr := item.value()
			match := searchQuery == "" ||
				strings.Contains(strings.ToLower(item.name), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(valStr), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(item.description), strings.ToLower(searchQuery))
			if match {
				filtered = append(filtered, item)
			}
		}

		// Keep selection index in bounds
		if selectedIdx >= len(filtered) {
			selectedIdx = len(filtered) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		// Build output buffer
		var buf strings.Builder
		buf.WriteString("\x1b[H") // move cursor to top-left

		// Draw Screen Title
		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("settings"))
		buf.WriteString("\n\n")

		// Draw Search Box
		searchLabelStyle := style.NewStyle().Foreground(theme.Text)
		buf.WriteString(searchLabelStyle.Render("  search:  "))
		
		searchValStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		buf.WriteString(searchValStyle.Render(searchQuery))
		buf.WriteString("\n")
		
		underlineStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(underlineStyle.Render("          ────────────────────"))
		buf.WriteString("\n\n")

		// Draw Settings List
		if len(filtered) == 0 {
			dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			buf.WriteString(dimStyle.Render("  (no matching settings found)"))
			buf.WriteString("\n")
		} else {
			for idx, item := range filtered {
				nameStr := item.name
				valStr := item.value()

				keyColWidth := 28
				nameLen := len(nameStr)
				leader := ""
				if nameLen < keyColWidth {
					leader = strings.Repeat("·", keyColWidth-nameLen)
				}

				if idx == selectedIdx {
					markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					nameStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
					bracketStyle := style.NewStyle().Foreground(theme.Secondary)

					valStrFormatted := fmt.Sprintf("%s %s %s", bracketStyle.Render("["), valStyle.Render(valStr), bracketStyle.Render("]"))
					buf.WriteString(fmt.Sprintf("%s  %s %s %s\n", markerStyle.Render("▸"), nameStyle.Render(nameStr), leaderStyle.Render(leader), valStrFormatted))
				} else {
					nameStyle := style.NewStyle().Foreground(theme.Text)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Secondary)
					buf.WriteString(fmt.Sprintf("   %s %s %s\n", nameStyle.Render(nameStr), leaderStyle.Render(leader), valStyle.Render(valStr)))
				}
			}
		}

		buf.WriteString("\n")

		// Draw Description of selected setting
		if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
			descStyle := style.NewStyle().Foreground(theme.Success)
			buf.WriteString(fmt.Sprintf("  %s\n", descStyle.Render(filtered[selectedIdx].description)))
		} else {
			buf.WriteString("\n")
		}

		buf.WriteString("\n")

		// Draw Color and Syntax Preview
		borderStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n  theme color preview:\n")

		primaryStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		secondaryStyle := style.NewStyle().Foreground(theme.Secondary)
		textStyle := style.NewStyle().Foreground(theme.Text)
		highlightStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		successStyle := style.NewStyle().Foreground(theme.Success)
		errorStyle := style.NewStyle().Foreground(theme.Error)

		buf.WriteString("    ")
		buf.WriteString(primaryStyle.Render("Primary"))
		buf.WriteString("  ")
		buf.WriteString(secondaryStyle.Render("Secondary"))
		buf.WriteString("  ")
		buf.WriteString(textStyle.Render("Text"))
		buf.WriteString("  ")
		buf.WriteString(highlightStyle.Render("Highlight"))
		buf.WriteString("  ")
		buf.WriteString(successStyle.Render("Success"))
		buf.WriteString("  ")
		buf.WriteString(errorStyle.Render("Error"))
		buf.WriteString("\n")

		var codePreviewBuf bytes.Buffer
		lang := "go"
		body := "package main\n\nfunc main() {\n\tprintln(\"Hello, World!\")\n}"
		chromaStyle := theme.ChromaStyle
		if chromaStyle == "" {
			chromaStyle = "friendly"
		}
		errHighlight := quick.Highlight(&codePreviewBuf, body, lang, "terminal16", chromaStyle)
		if errHighlight == nil {
			buf.WriteString("  syntax highlight preview:\n")
			previewLines := strings.Split(codePreviewBuf.String(), "\n")
			for _, line := range previewLines {
				if strings.TrimSpace(line) != "" {
					buf.WriteString("    " + line + "\n")
				}
			}
		}
		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n\n")

		// Draw Instructions
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter edit · esc clear search/exit")))
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("esc to cancel")))

		buf.WriteString("\x1b[J") // clear from here to end of screen

		// Write buffer to output, converting \n to \x1b[K\r\n for raw mode
		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		// Read key input
		var readBuf [16]byte
		n, err := rlInput.Read(readBuf[:])
		if err != nil {
			return nil, err
		}

		if n == 1 {
			char := readBuf[0]

			// Ctrl+C (Interrupt/Cancel) or Ctrl+D (Exit)
			if char == 3 || char == 4 {
				return nil, fmt.Errorf("cancelled")
			}

			// Enter Key (Edit / Toggle)
			if char == 13 || char == 10 {
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.isBool || len(item.options) > 0 {
						// instant action for boolean or option cycles
						if item.onToggle != nil {
							item.onToggle()
						}
					} else if item.onEdit != nil {
						// prompt editing for text/numeric fields
						term.Restore(fd, oldState)
						fmt.Fprint(rlOutput, "\x1b[?25h") // Show cursor

						fmt.Fprintf(rlOutput, "\r\n\r\n  edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  enter new value: ")

						reader := bufio.NewReader(rlInput)
						newVal, err := reader.ReadString('\n')
						if err == nil {
							newVal = strings.TrimSpace(newVal)
							err = item.onEdit(newVal)
							if err != nil {
								fmt.Fprintf(rlOutput, "\r\n  error: %v. press enter to continue...", err)
								_, _ = reader.ReadString('\n')
							}
						}

						// Restore raw mode and hide cursor
						fmt.Fprint(rlOutput, "\x1b[?25l")
						rawState, err := term.MakeRaw(fd)
						if err == nil {
							oldState = rawState
						}
					}
				}
				continue
			}

			// Escape Key (Clear Search / Exit)
			if char == 27 {
				if searchQuery != "" {
					searchQuery = ""
				} else {
					// cancelled/exited, return the modified configuration clone
					return &cloned, nil
				}
				continue
			}

			// Backspace Key
			if char == 127 || char == 8 {
				if len(searchQuery) > 0 {
					searchQuery = searchQuery[:len(searchQuery)-1]
				}
				continue
			}

			// Standard printable characters
			if char >= 32 && char <= 126 {
				searchQuery += string(char)
				continue
			}
		}

		// Parse multi-byte key escape sequences (like arrows)
		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A': // Arrow Up
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
				}
			case 'B': // Arrow Down
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx + 1) % len(filtered)
				}
			}
		}
	}
}

func ChooseSession(w io.Writer, sessions []db.SessionInfo, rlInput io.Reader, rlOutput io.Writer) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found")
	}

	fmt.Fprintln(rlOutput, "\navailable past sessions:")
	for i, s := range sessions {
		previewText := s.Preview
		if len(previewText) > 40 {
			previewText = previewText[:40] + "..."
		}
		fmt.Fprintf(rlOutput, "  [%d] %s (%d msgs) - %s\n", i+1, s.Timestamp[:16], s.MsgCount, previewText)
	}
	fmt.Fprintln(rlOutput)

	reader := bufio.NewReader(rlInput)
	for {
		fmt.Fprintf(rlOutput, "enter session number to load (1 to %d) or press enter to cancel: ", len(sessions))
		choiceStr, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		choiceStr = strings.TrimSpace(choiceStr)
		if choiceStr == "" {
			return "", fmt.Errorf("selection cancelled")
		}

		num, err := strconv.Atoi(choiceStr)
		if err != nil || num < 1 || num > len(sessions) {
			fmt.Fprintln(rlOutput, "error: invalid session number.")
			continue
		}
		return sessions[num-1].SessionID, nil
	}
}

func RunSessionExplorer(theme UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprintln(rlOutput, "\n==================================================")
		fmt.Fprintln(rlOutput, "               maquis sessions                 ")
		fmt.Fprintln(rlOutput, "==================================================")
		if len(sessions) == 0 {
			fmt.Fprintln(rlOutput, "no past sessions found.")
		} else {
			for i, s := range sessions {
				preview := s.Preview
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				fmt.Fprintf(rlOutput, "  [%d] %s (%d messages) - %s\n", i+1, s.Timestamp[:16], s.MsgCount, preview)
			}
		}
		fmt.Fprintln(rlOutput, "--------------------------------------------------")
		fmt.Fprintln(rlOutput, "options:")
		fmt.Fprintln(rlOutput, "  <number>     : load session")
		fmt.Fprintln(rlOutput, "  d <number>   : delete session")
		fmt.Fprintln(rlOutput, "  n            : start a new session")
		fmt.Fprintln(rlOutput, "  q            : cancel and return")
		fmt.Fprint(rlOutput, "\nchoose option: ")

		reader := bufio.NewReader(rlInput)
		inputStr, err := reader.ReadString('\n')
		if err != nil {
			return "", false, err
		}
		inputStr = strings.TrimSpace(inputStr)

		if inputStr == "" {
			continue
		}
		if inputStr == "q" || inputStr == "exit" {
			return "", false, fmt.Errorf("cancelled")
		}
		if inputStr == "n" {
			return "", true, nil
		}

		if strings.HasPrefix(inputStr, "d ") {
			numStr := strings.TrimSpace(strings.TrimPrefix(inputStr, "d "))
			num, err := strconv.Atoi(numStr)
			if err != nil || num < 1 || num > len(sessions) {
				fmt.Fprintln(rlOutput, "invalid session number to delete.")
				continue
			}
			sessionID := sessions[num-1].SessionID
			err = db.ClearSession(sessionID)
			if err != nil {
				fmt.Fprintf(rlOutput, "failed to delete session: %v\n", err)
			} else {
				fmt.Fprintf(rlOutput, "deleted session %s successfully.\n", sessionID)
			}
			continue
		}

		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(sessions) {
			return sessions[num-1].SessionID, false, nil
		}

		fmt.Fprintln(rlOutput, "invalid choice. please try again.")
	}
}

// RunInteractiveAgentManager opens an interactive terminal UI for managing multi-agents
func RunInteractiveAgentManager(mam *agent.MultiAgentManager, theme UITheme, rlInput io.Reader, rlOutput io.Writer) error {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return fmt.Errorf("not a terminal")
	}

	initialState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, initialState)

	// Save screen state, switch to alternate screen, and hide cursor
	fmt.Fprint(rlOutput, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(rlOutput, "\x1b[?25h\x1b[?1049l")

	selectedIdx := 0

	for {
		// Fetch current list of agents
		agentsList := mam.ListAgents()
		activeName := mam.ActiveAgentName()

		// Build combined list of agents
		list := append([]string{"base"}, agentsList...)

		if selectedIdx >= len(list) {
			selectedIdx = len(list) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		// Build output buffer
		var buf strings.Builder
		buf.WriteString("\x1b[H") // move cursor to top-left

		// Draw Screen Title
		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("agent swarm manager"))
		buf.WriteString("\n\n")

		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		keyStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		valStyle := style.NewStyle().Foreground(theme.Text)
		highlightStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

		// 1. Prepare Left Column (Active Agents List)
		var leftLines []string
		leftLines = append(leftLines, headerStyle.Render("  active swarm nodes:"))
		leftLines = append(leftLines, style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────"))

		for idx, name := range list {
			marker := "  "
			if name == activeName || (name == "base" && activeName == "") {
				marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
			}

			var nameStr string
			if idx == selectedIdx {
				nameStr = style.NewStyle().Foreground(theme.Primary).Bold(true).Render(name)
			} else {
				nameStr = style.NewStyle().Foreground(theme.Text).Render(name)
			}

			leftLines = append(leftLines, fmt.Sprintf("   %s%s", marker, nameStr))
		}

		// 2. Prepare Right Column (Selected Node Info)
		selectedName := list[selectedIdx]
		var sysPrompt string
		var parentStr string
		var skillStr string
		var typeStr string

		isFocused := (selectedName == activeName)
		if selectedName == "base" && activeName == "" {
			isFocused = true
		}
		focusVal := valStyle.Render("No (press Enter to focus & chat)")
		if isFocused {
			focusVal = highlightStyle.Render("Yes (currently chatting)")
		}

		if selectedName == "base" {
			typeStr = "Default Base Agent"
			parentStr = "none"
			skillStr = "Generic (all active skills)"
			sysPrompt = mam.BaseAgent.GetSystemPrompt()
		} else {
			typeStr = "Swarm Subagent"
			parentName := mam.GetParentName(selectedName)
			if parentName == "" {
				parentStr = "base"
			} else {
				parentStr = parentName
			}

			skills, _ := mam.ListAgentSkills(selectedName)
			var skillNames []string
			for _, s := range skills {
				skillNames = append(skillNames, s.Name)
			}
			if len(skillNames) > 0 {
				skillStr = strings.Join(skillNames, ", ")
			} else {
				skillStr = "Generic"
			}
			sysPrompt = mam.GetAgentSystemPrompt(selectedName)
		}

		var rightLines []string
		rightLines = append(rightLines, headerStyle.Render("selected node info:"))
		rightLines = append(rightLines, style.NewStyle().Foreground(theme.Border).Render("─────────────────────────────────────────"))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Name:"), valStyle.Render(selectedName)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Type:"), valStyle.Render(typeStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Parent:"), valStyle.Render(parentStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Skill:"), valStyle.Render(skillStr)))
		rightLines = append(rightLines, fmt.Sprintf("  %s %s", keyStyle.Render("Focus:"), focusVal))
		rightLines = append(rightLines, fmt.Sprintf("  %s", keyStyle.Render("Instructions / Goal:")))

		// Wrap and truncate system prompt
		wrappedLines := wrapText(sysPrompt, 45)
		for i := 0; i < 8 && i < len(wrappedLines); i++ {
			rightLines = append(rightLines, "  "+valStyle.Render(wrappedLines[i]))
		}
		if len(wrappedLines) > 8 {
			rightLines = append(rightLines, "  "+style.NewStyle().Foreground(theme.Border).Italic(true).Render("... (truncated)"))
		}

		// 3. Render Columns Side-by-Side
		maxLines := len(leftLines)
		if len(rightLines) > maxLines {
			maxLines = len(rightLines)
		}

		width, _ := getTerminalSize()
		useSideBySide := width >= 80

		if useSideBySide {
			for i := 0; i < maxLines; i++ {
				var leftPart string
				if i < len(leftLines) {
					leftPart = leftLines[i]
				}
				leftLen := len(stripAnsi(leftPart))
				leftPad := ""
				if leftLen < 38 {
					leftPad = strings.Repeat(" ", 38-leftLen)
				}

				var rightPart string
				if i < len(rightLines) {
					rightPart = rightLines[i]
				}

				buf.WriteString(fmt.Sprintf("%s%s │ %s\n", leftPart, leftPad, rightPart))
			}
		} else {
			// Stacked layout for narrow terminals
			for _, line := range leftLines {
				buf.WriteString(line + "\n")
			}
			buf.WriteString(style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────\n"))
			for _, line := range rightLines {
				buf.WriteString(line + "\n")
			}
		}

		// Draw Controls Bar
		navStyle := style.NewStyle().Foreground(theme.Border)
		keyStyleBar := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString("\n")
		buf.WriteString(navStyle.Render("  controls: "))
		buf.WriteString(fmt.Sprintf("%s Focus & Chat   ", keyStyleBar.Render("[Enter]")))
		buf.WriteString(fmt.Sprintf("%s Create Agent   ", keyStyleBar.Render("[C]")))
		buf.WriteString(fmt.Sprintf("%s Delete Agent   ", keyStyleBar.Render("[D]")))
		buf.WriteString(fmt.Sprintf("%s Exit Swarm Manager\n", keyStyleBar.Render("[Esc]")))

		buf.WriteString("\x1b[J") // clear to end

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		// Read key input
		var readBuf [16]byte
		n, errReader := rlInput.Read(readBuf[:])
		if errReader != nil {
			return errReader
		}

		if n == 1 {
			char := readBuf[0]

			// Escape / Ctrl+C / Ctrl+D
			if char == 3 || char == 27 || char == 4 {
				return nil
			}

			// Enter
			if char == 13 || char == 10 {
				mam.JoinAgent(selectedName)
				term.Restore(fd, initialState)
				fmt.Fprint(rlOutput, "\x1b[?25h") // Show cursor
				fmt.Fprintf(rlOutput, "\r\nSwitched active chat focus to agent '%s'.\r\nPress enter to continue...", selectedName)
				_, _ = bufio.NewReader(rlInput).ReadString('\n')
				return nil
			}

			// Create New (C/c)
			if char == 'c' || char == 'C' {
				term.Restore(fd, initialState)
				fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J") // Show cursor, clear screen

				fmt.Fprint(rlOutput, "=== Create New Swarm Agent ===\r\n\r\n")
				
				reader := bufio.NewReader(rlInput)
				
				fmt.Fprint(rlOutput, "Enter unique agent name (e.g. devops, tester): ")
				agentName, errInput := reader.ReadString('\n')
				if errInput == nil {
					agentName = strings.TrimSpace(agentName)
					if agentName == "" {
						fmt.Fprint(rlOutput, "\r\nError: Agent name cannot be empty. Press enter to continue...")
						_, _ = reader.ReadString('\n')
					} else {
						fmt.Fprint(rlOutput, "Enter system instructions / goals: ")
						sysPrompt, errInput2 := reader.ReadString('\n')
						if errInput2 == nil {
							sysPrompt = strings.TrimSpace(sysPrompt)
							if sysPrompt == "" {
								fmt.Fprint(rlOutput, "\r\nError: Instructions cannot be empty. Press enter to continue...")
								_, _ = reader.ReadString('\n')
							} else {
								fmt.Fprint(rlOutput, "\x1b[?25l")
								_, _ = term.MakeRaw(fd)

								// Parent Agent Select
								parentOptions := []string{"None (Base)"}
								for _, name := range agentsList {
									parentOptions = append(parentOptions, name)
								}
								parentIdx, errSelect := runInteractiveSelect(rlInput, rlOutput, "Select Parent Agent (default: None):", parentOptions, theme)
								if errSelect == nil {
									parentName := ""
									if parentIdx > 0 {
										parentName = parentOptions[parentIdx]
									}

									// Dedicated Skill Select
									skillOptions := []string{"Generic (All Active Skills)"}
									for _, s := range mam.BaseAgent.ActiveSkills {
										skillOptions = append(skillOptions, s.Name)
									}
									skillIdx, errSelect2 := runInteractiveSelect(rlInput, rlOutput, "Select Dedicated Reference Skill:", skillOptions, theme)
									if errSelect2 == nil {
										skillName := ""
										if skillIdx > 0 {
											skillName = skillOptions[skillIdx]
										}

										errSpawn := mam.SpawnAgent(agentName, sysPrompt, parentName, skillName)

										term.Restore(fd, initialState)
										fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J") // Show cursor, clear screen
										if errSpawn != nil {
											fmt.Fprintf(rlOutput, "Error spawning agent: %v\r\n\r\nPress enter to continue...", errSpawn)
										} else {
											fmt.Fprintf(rlOutput, "Successfully spawned agent '%s'!\r\n\r\nPress enter to continue...", agentName)
										}
										_, _ = reader.ReadString('\n')
									}
								}
							}
						}
					}
				}

				fmt.Fprint(rlOutput, "\x1b[?25l")
				_, _ = term.MakeRaw(fd)
				continue
			}

			// Delete (D/d)
			if char == 'd' || char == 'D' {
				if selectedName == "base" {
					term.Restore(fd, initialState)
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J") // Show cursor, clear screen
					fmt.Fprint(rlOutput, "Error: Cannot delete default base agent.\r\n\r\nPress enter to continue...")
					_, _ = bufio.NewReader(rlInput).ReadString('\n')
					fmt.Fprint(rlOutput, "\x1b[?25l")
					_, _ = term.MakeRaw(fd)
				} else {
					term.Restore(fd, initialState)
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J") // Show cursor, clear screen
					fmt.Fprintf(rlOutput, "Are you sure you want to delete agent '%s'? [y/N]: ", selectedName)
					
					reader := bufio.NewReader(rlInput)
					confirm, errInput := reader.ReadString('\n')
					if errInput == nil && (strings.HasPrefix(strings.ToLower(strings.TrimSpace(confirm)), "y")) {
						errKill := mam.KillAgent(selectedName)
						fmt.Fprint(rlOutput, "\x1b[H\x1b[2J")
						if errKill != nil {
							fmt.Fprintf(rlOutput, "Error terminating agent: %v\r\n\r\nPress enter to continue...", errKill)
						} else {
							fmt.Fprintf(rlOutput, "Agent '%s' terminated and deleted.\r\n\r\nPress enter to continue...", selectedName)
						}
						_, _ = reader.ReadString('\n')
					}

					fmt.Fprint(rlOutput, "\x1b[?25l")
					_, _ = term.MakeRaw(fd)

					// Adjust selection index
					if selectedIdx >= len(list)-1 {
						selectedIdx = len(list) - 2
						if selectedIdx < 0 {
							selectedIdx = 0
						}
					}
				}
				continue
			}
		}

		// Arrow keys navigation
		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A': // Arrow Up
				selectedIdx = (selectedIdx - 1 + len(list)) % len(list)
			case 'B': // Arrow Down
				selectedIdx = (selectedIdx + 1) % len(list)
			}
		}
	}
}


// runInteractiveSelect shows a simple interactive list selection screen
func runInteractiveSelect(rlInput io.Reader, rlOutput io.Writer, title string, items []string, theme UITheme) (int, error) {
	selectedIdx := 0

	for {
		var buf strings.Builder
		buf.WriteString("\x1b[H") // move cursor to top-left

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render(title))
		buf.WriteString("\n\n")

		for idx, item := range items {
			if idx == selectedIdx {
				markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
				itemStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
				buf.WriteString(fmt.Sprintf("    %s %s\n", markerStyle.Render("▸"), itemStyle.Render(item)))
			} else {
				itemStyle := style.NewStyle().Foreground(theme.Text)
				buf.WriteString(fmt.Sprintf("      %s\n", itemStyle.Render(item)))
			}
		}

		buf.WriteString("\n")
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter select · esc cancel")))

		buf.WriteString("\x1b[J") // clear to end

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		var readBuf [16]byte
		n, err := rlInput.Read(readBuf[:])
		if err != nil {
			return -1, err
		}

		if n == 1 {
			char := readBuf[0]
			if char == 3 || char == 27 || char == 4 {
				return -1, fmt.Errorf("selection cancelled")
			}
			if char == 13 || char == 10 {
				return selectedIdx, nil
			}
		}

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A': // Arrow Up
				selectedIdx = (selectedIdx - 1 + len(items)) % len(items)
			case 'B': // Arrow Down
				selectedIdx = (selectedIdx + 1) % len(items)
			}
		}
	}
}
