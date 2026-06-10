package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui/style"
)

func AskForApproval(w io.Writer, theme UITheme) (bool, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	var input io.Reader = os.Stdin
	var output io.Writer = os.Stdout
	var fd int = int(os.Stdin.Fd())
	if err == nil {
		defer tty.Close()
		input = tty
		output = tty
		fd = int(tty.Fd())
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	fmt.Fprint(output, promptStyle.Render(" Approve tool execution? [y/N/a (always)]: "))

	isTerm := term.IsTerminal(fd)
	if isTerm {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	buf := make([]byte, 1)
	n, err := input.Read(buf)
	if err != nil || n == 0 {
		fmt.Fprintln(output)
		return false, false
	}

	char := buf[0]
	// Handle Ctrl+C (3) or Esc (27)
	if char == 3 || char == 27 {
		fmt.Fprintln(output, "rejected")
		return false, false
	}

	if char == 'y' || char == 'Y' {
		fmt.Fprintln(output, "y")
		return true, false
	} else if char == 'a' || char == 'A' {
		fmt.Fprintln(output, "always")
		return true, true
	} else {
		if char == '\r' || char == '\n' {
			fmt.Fprintln(output, "n")
		} else {
			fmt.Fprintf(output, "%c\n", char)
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
			id:          "endpoint",
			name:        "Endpoint URL",
			value:       func() string { return cloned.Endpoint },
			description: "API endpoint URL for the LLM service",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				cloned.Endpoint = newVal
				return nil
			},
		},
		{
			id:          "model",
			name:        "Model Name",
			value:       func() string { return cloned.Model },
			description: "Name of the LLM model to use",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				cloned.Model = newVal
				return nil
			},
		},
		{
			id:          "temperature",
			name:        "Temperature",
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
			name:        "Visual Theme",
			value:       func() string { return cloned.Theme },
			description: "Visual aesthetic style of the terminal theme",
			options:     []string{"dark", "neon", "light", "gruvbox"},
			onToggle: func() {
				themes := []string{"dark", "neon", "light", "gruvbox"}
				idx := -1
				for i, t := range themes {
					if strings.ToLower(cloned.Theme) == t {
						idx = i
						break
					}
				}
				nextIdx := (idx + 1) % len(themes)
				cloned.Theme = themes[nextIdx]
				theme = GetTheme(cloned.Theme) // dynamically apply theme visually in real-time
			},
		},
		{
			id:          "auto_approve",
			name:        "Auto-Approve",
			value:       func() string { return formatBool(cloned.AutoApprove) },
			description: "Automatically approve all tool execution prompts",
			isBool:      true,
			onToggle: func() {
				cloned.AutoApprove = !cloned.AutoApprove
				cloned.YoloMode = cloned.AutoApprove
			},
		},
		{
			id:          "show_thinking",
			name:        "Show Thinking",
			value:       func() string { return formatBool(cloned.ShowThinking) },
			description: "Stream the LLM thinking/reasoning process",
			isBool:      true,
			onToggle: func() {
				cloned.ShowThinking = !cloned.ShowThinking
			},
		},
		{
			id:          "collapse_results",
			name:        "Collapse Results",
			value:       func() string { return formatBool(cloned.CollapseResults) },
			description: "Collapse tool results output to keep history clean",
			isBool:      true,
			onToggle: func() {
				cloned.CollapseResults = !cloned.CollapseResults
			},
		},
		{
			id:          "show_tokens",
			name:        "Show Tokens",
			value:       func() string { return formatBool(cloned.ShowTokens) },
			description: "Display token usage metrics under each response",
			isBool:      true,
			onToggle: func() {
				cloned.ShowTokens = !cloned.ShowTokens
			},
		},
		{
			id:          "context_window_limit",
			name:        "Context Limit",
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
			id:          "direct_commands",
			name:        "Direct Commands",
			value:       func() string { return formatBool(cloned.DirectCommands) },
			description: "Enable direct execution of local shell commands",
			isBool:      true,
			onToggle: func() {
				cloned.DirectCommands = !cloned.DirectCommands
			},
		},
		{
			id:          "cert_file",
			name:        "Client Cert",
			value:       func() string { return cloned.CertFile },
			description: "Path to the SSL client certificate file",
			onEdit: func(newVal string) error {
				cloned.CertFile = newVal
				return nil
			},
		},
		{
			id:          "key_file",
			name:        "Client Key",
			value:       func() string { return cloned.KeyFile },
			description: "Path to the SSL client private key file",
			onEdit: func(newVal string) error {
				cloned.KeyFile = newVal
				return nil
			},
		},
		{
			id:          "skip_verify",
			name:        "Skip SSL Verify",
			value:       func() string { return formatBool(cloned.SkipVerify) },
			description: "Skip SSL/TLS certificate verification",
			isBool:      true,
			onToggle: func() {
				cloned.SkipVerify = !cloned.SkipVerify
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
		buf.WriteString(titleStyle.Render("Settings"))
		buf.WriteString("\n\n")

		// Draw Search Box
		searchLabelStyle := style.NewStyle().Foreground(theme.Text)
		buf.WriteString(searchLabelStyle.Render("  Search:  "))
		
		searchValStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		buf.WriteString(searchValStyle.Render(searchQuery))
		buf.WriteString("\n")
		
		underlineStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(underlineStyle.Render("          ────────────────────"))
		buf.WriteString("\n\n")

		// Draw Settings List
		if len(filtered) == 0 {
			dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			buf.WriteString(dimStyle.Render("  (No matching settings found)"))
			buf.WriteString("\n")
		} else {
			for idx, item := range filtered {
				marker := " "
				if idx == selectedIdx {
					marker = ">"
				}

				nameStr := item.name
				valStr := item.value()

				if idx == selectedIdx {
					markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					nameStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					valStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
					buf.WriteString(fmt.Sprintf("%s %-24s %s\n", markerStyle.Render(marker), nameStyle.Render(nameStr), valStyle.Render(valStr)))
				} else {
					nameStyle := style.NewStyle().Foreground(theme.Text)
					valStyle := style.NewStyle().Foreground(theme.Secondary)
					buf.WriteString(fmt.Sprintf("%s %-24s %s\n", marker, nameStyle.Render(nameStr), valStyle.Render(valStr)))
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

		// Draw Instructions
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ Navigate · enter Edit · Esc Clear Search/Exit")))
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

			// Ctrl+C (Interrupt/Cancel)
			if char == 3 {
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

						fmt.Fprintf(rlOutput, "\r\n\r\n  Edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  Enter new value: ")

						reader := bufio.NewReader(rlInput)
						newVal, err := reader.ReadString('\n')
						if err == nil {
							newVal = strings.TrimSpace(newVal)
							err = item.onEdit(newVal)
							if err != nil {
								fmt.Fprintf(rlOutput, "\r\n  Error: %v. Press Enter to continue...", err)
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

	fmt.Fprintln(rlOutput, "\nAvailable Past Sessions:")
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
		fmt.Fprintf(rlOutput, "Enter session number to load (1 to %d) or press Enter to cancel: ", len(sessions))
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
			fmt.Fprintln(rlOutput, "Error: invalid session number.")
			continue
		}
		return sessions[num-1].SessionID, nil
	}
}

func RunMultilineEditor(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprintln(rlOutput, "=== Multiline Prompt Editor ===")
	fmt.Fprintln(rlOutput, "Type or paste your prompt here. When finished, type '.' on a line by itself and press Enter.")
	fmt.Fprintln(rlOutput, "Type 'cancel' to abort.")

	var lines []string
	scanner := bufio.NewScanner(rlInput)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "." {
			break
		}
		if line == "cancel" && len(lines) == 0 {
			return "", fmt.Errorf("multiline editor cancelled")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func RunSessionExplorer(theme UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprintln(rlOutput, "\n==================================================")
		fmt.Fprintln(rlOutput, "               BIDOUILLE SESSIONS                 ")
		fmt.Fprintln(rlOutput, "==================================================")
		if len(sessions) == 0 {
			fmt.Fprintln(rlOutput, "No past sessions found.")
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
		fmt.Fprintln(rlOutput, "Options:")
		fmt.Fprintln(rlOutput, "  <number>     : Load session")
		fmt.Fprintln(rlOutput, "  d <number>   : Delete session")
		fmt.Fprintln(rlOutput, "  n            : Start a new session")
		fmt.Fprintln(rlOutput, "  q            : Cancel and return")
		fmt.Fprint(rlOutput, "\nChoose option: ")

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
				fmt.Fprintln(rlOutput, "Invalid session number to delete.")
				continue
			}
			sessionID := sessions[num-1].SessionID
			err = db.ClearSession(sessionID)
			if err != nil {
				fmt.Fprintf(rlOutput, "Failed to delete session: %v\n", err)
			} else {
				fmt.Fprintf(rlOutput, "Deleted session %s successfully.\n", sessionID)
			}
			continue
		}

		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(sessions) {
			return sessions[num-1].SessionID, false, nil
		}

		fmt.Fprintln(rlOutput, "Invalid choice. Please try again.")
	}
}
