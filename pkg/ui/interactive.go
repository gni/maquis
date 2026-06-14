package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"
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
			id:          "endpoint",
			name:        "endpoint url",
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
			name:        "model name",
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

func RunMultilineEditor(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprintln(rlOutput, "=== multiline prompt editor ===")
	fmt.Fprintln(rlOutput, "type or paste your prompt here. when finished, type '.' on a line by itself and press enter.")
	fmt.Fprintln(rlOutput, "type 'cancel' to abort.")

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
		fmt.Fprintln(rlOutput, "               bidouille sessions                 ")
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
