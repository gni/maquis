package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	fd := int(os.Stdin.Fd())

	if w != nil {
		output = w
		if tty, ok := w.(*os.File); ok {
			input = tty
			output = tty
			fd = int(tty.Fd())
		}
	}

	getUI().StateMu.Lock()
	hasActiveReader := getUI().ActiveInputReader != nil
	if hasActiveReader {
		input = getUI().ActiveInputReader
		output = w
	}
	getUI().StateMu.Unlock()

	if !hasActiveReader && input == os.Stdin {
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			input = tty
			output = tty
			fd = int(tty.Fd())
		}
	}

	isTerm := term.IsTerminal(fd)
	var oldState *term.State
	if isTerm && !hasActiveReader {
		var err error
		oldState, err = term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	// We no longer fallback to single byte if hasActiveReader is true. We support dropdown!
	// We only fallback if it's NOT a terminal AND no active reader
	if !isTerm && !hasActiveReader {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		fmt.Fprint(output, promptStyle.Render(" approve tool execution? [y/n/a (always)]: "))
		buf := make([]byte, 1)
		input.Read(buf)
		char := buf[0]
		if char == 'y' || char == 'Y' {
			return true, false
		} else if char == 'a' || char == 'A' {
			return true, true
		}
		return false, false
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	activeStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	options := []string{"yes", "no", "always"}
	selected := 0

	firstRender := true
	fmt.Fprint(os.Stdout, "\x1b[?25l") // Hide cursor
	defer fmt.Fprint(os.Stdout, "\x1b[?25h") // Ensure cursor is restored

	renderMenu := func() {
		if firstRender {
			fmt.Fprint(output, "\r\x1b[K", promptStyle.Render(" approve tool execution?\r\n"))
			for i, opt := range options {
				fmt.Fprint(output, "\r\x1b[K")
				if i == selected {
					fmt.Fprintf(output, "  > %s\r\n", activeStyle.Render(opt))
				} else {
					fmt.Fprintf(output, "    %s\r\n", opt)
				}
			}
			firstRender = false
		} else {
			fmt.Fprintf(os.Stdout, "\x1b[%dA", len(options)+1)
			fmt.Fprint(os.Stdout, "\r\x1b[K", promptStyle.Render(" approve tool execution?\r\n"))
			for i, opt := range options {
				fmt.Fprint(os.Stdout, "\r\x1b[K")
				if i == selected {
					fmt.Fprintf(os.Stdout, "  > %s\r\n", activeStyle.Render(opt))
				} else {
					fmt.Fprintf(os.Stdout, "    %s\r\n", opt)
				}
			}
		}
	}

	clearMenu := func() {
		// Do nothing! We leave the menu on screen. 
		// ncw.count has correctly counted the lines, so RenderToolOutput will erase it perfectly!
	}

	renderMenu()

	buf := make([]byte, 3)
	for {
		n, err := input.Read(buf)
		if err != nil || n == 0 {
			continue
		}
		if n == 1 {
			char := buf[0]
			if char == '\r' || char == '\n' {
				break
			}
			if char == 3 || char == 4 || char == 27 {
				selected = 1 // default to no on esc/ctrl-c
				break
			}
			if char == 'y' || char == 'Y' {
				selected = 0
				break
			}
			if char == 'n' || char == 'N' {
				selected = 1
				break
			}
			if char == 'a' || char == 'A' {
				selected = 2
				break
			}
		} else if n == 3 && buf[0] == 27 && buf[1] == '[' {
			if buf[2] == 'A' { // up
				selected--
				if selected < 0 {
					selected = len(options) - 1
				}
			} else if buf[2] == 'B' { // down
				selected++
				if selected >= len(options) {
					selected = 0
				}
			}
		}
		renderMenu()
	}

	clearMenu()

	if selected == 0 {
		return true, false
	} else if selected == 2 {
		return true, true
	} else {
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	defer signal.Stop(sigChan)

	sr := newSessionReader(rlInput)
	defer sr.Close()

	fmt.Fprint(rlOutput, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(rlOutput, "\x1b[?25h\x1b[?1049l")

	formatBool := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}

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
				theme = GetConfiguredTheme(&cloned)
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
				theme = GetConfiguredTheme(&cloned)
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
			id:          "max_completion_tokens",
			name:        "max completion tokens",
			value:       func() string { return fmt.Sprintf("%d", cloned.MaxCompletionTokens) },
			description: "Maximum token limit for output/completion generations",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				tokens, err := strconv.Atoi(newVal)
				if err != nil || tokens <= 0 {
					return fmt.Errorf("must be a positive integer")
				}
				cloned.MaxCompletionTokens = tokens
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

		if selectedIdx >= len(filtered) {
			selectedIdx = len(filtered) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("settings"))
		buf.WriteString("\n\n")

		searchLabelStyle := style.NewStyle().Foreground(theme.Text)
		buf.WriteString(searchLabelStyle.Render("  search:  "))
		
		searchValStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		buf.WriteString(searchValStyle.Render(searchQuery))
		buf.WriteString("\n")
		
		underlineStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(underlineStyle.Render("           ────────────────────"))
		buf.WriteString("\n\n")

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

		if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
			descStyle := style.NewStyle().Foreground(theme.Success)
			buf.WriteString(fmt.Sprintf("  %s\n", descStyle.Render(filtered[selectedIdx].description)))
		} else {
			buf.WriteString("\n")
		}

		buf.WriteString("\n")

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

		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter edit · esc clear search/exit")))
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("esc to cancel")))

		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		readBuf, resized, err := sr.ReadKeyOrResize(sigChan)
		if err != nil {
			return nil, err
		}
		if resized {
			continue
		}
		n := len(readBuf)

		if n == 1 {
			char := readBuf[0]

			if char == 3 || char == 4 {
				return nil, fmt.Errorf("cancelled")
			}

			if char == 13 || char == 10 {
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.isBool || len(item.options) > 0 {
						if item.onToggle != nil {
							item.onToggle()
						}
					} else if item.onEdit != nil {
						fmt.Fprint(rlOutput, "\x1b[?25h")

						fmt.Fprintf(rlOutput, "\r\n\r\n  edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  enter new value: ")

						newVal, errRead := sr.ReadLine(rlOutput)
						if errRead == nil {
							newVal = strings.TrimSpace(newVal)
							err = item.onEdit(newVal)
							if err != nil {
								fmt.Fprintf(rlOutput, "\r\n  error: %v. press enter to continue...", err)
								_, _ = sr.ReadLine(rlOutput)
							}
						}

						fmt.Fprint(rlOutput, "\x1b[?25l")
					}
				}
				continue
			}

			if char == 27 {
				if searchQuery != "" {
					searchQuery = ""
				} else {
					return &cloned, nil
				}
				continue
			}

			if char == 127 || char == 8 {
				if len(searchQuery) > 0 {
					searchQuery = searchQuery[:len(searchQuery)-1]
				}
				continue
			}

			if char >= 32 && char <= 126 {
				searchQuery += string(char)
				continue
			}
		}

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
				}
			case 'B':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx + 1) % len(filtered)
				}
			}
		}
	}
}

func RunSessionExplorer(theme UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprintln(rlOutput, "\n==================================================")
		fmt.Fprintln(rlOutput, "                maquis sessions                  ")
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	defer signal.Stop(sigChan)

	sr := newSessionReader(rlInput)
	defer sr.Close()

	fmt.Fprint(rlOutput, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(rlOutput, "\x1b[?25h\x1b[?1049l")

	selectedIdx := 0

	for {
		agentsList := mam.ListAgents()
		activeName := mam.ActiveAgentName()

		list := append([]string{"base"}, agentsList...)

		if selectedIdx >= len(list) {
			selectedIdx = len(list) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("agent swarm manager"))
		buf.WriteString("\n\n")

		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		keyStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		valStyle := style.NewStyle().Foreground(theme.Text)
		highlightStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

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

		wrappedLines := wrapText(sysPrompt, 45)
		for i := 0; i < 8 && i < len(wrappedLines); i++ {
			rightLines = append(rightLines, "  "+valStyle.Render(wrappedLines[i]))
		}
		if len(wrappedLines) > 8 {
			rightLines = append(rightLines, "  "+style.NewStyle().Foreground(theme.Border).Italic(true).Render("... (truncated)"))
		}

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
			for _, line := range leftLines {
				buf.WriteString(line + "\n")
			}
			buf.WriteString(style.NewStyle().Foreground(theme.Border).Render("  ───────────────────────────────────\n"))
			for _, line := range rightLines {
				buf.WriteString(line + "\n")
			}
		}

		navStyle := style.NewStyle().Foreground(theme.Border)
		keyStyleBar := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString("\n")
		buf.WriteString(navStyle.Render("  controls: "))
		buf.WriteString(fmt.Sprintf("%s Focus & Chat   ", keyStyleBar.Render("[Enter]")))
		buf.WriteString(fmt.Sprintf("%s Create Agent   ", keyStyleBar.Render("[C]")))
		buf.WriteString(fmt.Sprintf("%s Delete Agent   ", keyStyleBar.Render("[D]")))
		buf.WriteString(fmt.Sprintf("%s Exit Swarm Manager\n", keyStyleBar.Render("[Esc]")))

		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		readBuf, resized, errReader := sr.ReadKeyOrResize(sigChan)
		if errReader != nil {
			return errReader
		}
		if resized {
			continue
		}
		n := len(readBuf)

		if n == 1 {
			char := readBuf[0]

			if char == 3 || char == 27 || char == 4 {
				return nil
			}

			if char == 13 || char == 10 {
				mam.JoinAgent(selectedName)
				fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
				fmt.Fprintf(rlOutput, "Switched active chat focus to agent '%s'.\r\nPress enter to continue...", selectedName)
				_, _ = sr.ReadLine(rlOutput)
				return nil
			}

			if char == 'c' || char == 'C' {
				fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")

				fmt.Fprint(rlOutput, "=== Create New Swarm Agent ===\r\n\r\n")

				fmt.Fprint(rlOutput, "Enter unique agent name (e.g. devops, tester): ")
				agentName, errInput := sr.ReadLine(rlOutput)
				if errInput == nil {
					agentName = strings.TrimSpace(agentName)
					if agentName == "" {
						fmt.Fprint(rlOutput, "\r\nError: Agent name cannot be empty. Press enter to continue...")
						_, _ = sr.ReadLine(rlOutput)
					} else {
						fmt.Fprint(rlOutput, "Enter system instructions / goals: ")
						sysPrompt, errInput2 := sr.ReadLine(rlOutput)
						if errInput2 == nil {
							sysPrompt = strings.TrimSpace(sysPrompt)
							if sysPrompt == "" {
								fmt.Fprint(rlOutput, "\r\nError: Instructions cannot be empty. Press enter to continue...")
								_, _ = sr.ReadLine(rlOutput)
							} else {
								fmt.Fprint(rlOutput, "\x1b[?25l")

								parentOptions := []string{"None (Base)"}
								for _, name := range agentsList {
									parentOptions = append(parentOptions, name)
								}
								parentIdx, errSelect := runInteractiveSelect(sr, sigChan, rlOutput, "Select Parent Agent (default: None):", parentOptions, theme)
								if errSelect == nil {
									parentName := ""
									if parentIdx > 0 {
										parentName = parentOptions[parentIdx]
									}

									skillOptions := []string{"Generic (All Active Skills)"}
									for _, s := range mam.BaseAgent.ActiveSkills {
										skillOptions = append(skillOptions, s.Name)
									}
									skillIdx, errSelect2 := runInteractiveSelect(sr, sigChan, rlOutput, "Select Dedicated Reference Skill:", skillOptions, theme)
									if errSelect2 == nil {
										skillName := ""
										if skillIdx > 0 {
											skillName = skillOptions[skillIdx]
										}

										errSpawn := mam.SpawnAgent(agentName, sysPrompt, parentName, skillName)

										fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
										if errSpawn != nil {
											fmt.Fprintf(rlOutput, "Error spawning agent: %v\r\n\r\nPress enter to continue...", errSpawn)
										} else {
											fmt.Fprintf(rlOutput, "Successfully spawned agent '%s'!\r\n\r\nPress enter to continue...", agentName)
										}
										_, _ = sr.ReadLine(rlOutput)
									}
								}
							}
						}
					}
				}

				fmt.Fprint(rlOutput, "\x1b[?25l")
				continue
			}

			if char == 'd' || char == 'D' {
				if selectedName == "base" {
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
					fmt.Fprint(rlOutput, "Error: Cannot delete default base agent.\r\n\r\nPress enter to continue...")
					_, _ = sr.ReadLine(rlOutput)
					fmt.Fprint(rlOutput, "\x1b[?25l")
				} else {
					fmt.Fprint(rlOutput, "\x1b[?25h\x1b[H\x1b[2J")
					fmt.Fprintf(rlOutput, "Are you sure you want to delete agent '%s'? [y/N]: ", selectedName)

					confirm, errInput := sr.ReadLine(rlOutput)
					if errInput == nil && (strings.HasPrefix(strings.ToLower(strings.TrimSpace(confirm)), "y")) {
						errKill := mam.RemoveAgent(selectedName)
						fmt.Fprint(rlOutput, "\x1b[H\x1b[2J")
						if errKill != nil {
							fmt.Fprintf(rlOutput, "Error terminating agent: %v\r\n\r\nPress enter to continue...", errKill)
						} else {
							fmt.Fprintf(rlOutput, "Agent '%s' terminated and deleted.\r\n\r\nPress enter to continue...", selectedName)
						}
						_, _ = sr.ReadLine(rlOutput)
					}

					fmt.Fprint(rlOutput, "\x1b[?25l")

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

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A':
				selectedIdx = (selectedIdx - 1 + len(list)) % len(list)
			case 'B':
				selectedIdx = (selectedIdx + 1) % len(list)
			}
		}
	}
}

func runInteractiveSelect(sr *sessionReader, sigChan chan os.Signal, rlOutput io.Writer, title string, items []string, theme UITheme) (int, error) {
	selectedIdx := 0

	for {
		var buf strings.Builder
		buf.WriteString("\x1b[H")

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

		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		readBuf, resized, err := sr.ReadKeyOrResize(sigChan)
		if err != nil {
			return -1, err
		}
		if resized {
			continue
		}
		n := len(readBuf)

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
			case 'A':
				selectedIdx = (selectedIdx - 1 + len(items)) % len(items)
			case 'B':
				selectedIdx = (selectedIdx + 1) % len(items)
			}
		}
	}
}

func readInputRaw(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprint(rlOutput, "\x1b[?25h")
	defer fmt.Fprint(rlOutput, "\x1b[?25l")

	var sb strings.Builder
	var buf [1024]byte
	for {
		n, err := rlInput.Read(buf[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}

		for i := 0; i < n; i++ {
			char := buf[i]

			if char == 13 || char == 10 {
				fmt.Fprint(rlOutput, "\r\n")
				return sb.String(), nil
			}

			if char == 127 || char == 8 {
				str := sb.String()
				if len(str) > 0 {
					sb.Reset()
					sb.WriteString(str[:len(str)-1])
					fmt.Fprint(rlOutput, "\b \b")
				}
				continue
			}

			if char == 3 || char == 4 {
				return "", fmt.Errorf("cancelled")
			}

			if char >= 32 && char <= 126 {
				sb.WriteByte(char)
				fmt.Fprint(rlOutput, string(char))
			}
		}
	}
}

type readResult struct {
	data []byte
	err  error
}

type sessionReader struct {
	chanInput chan byte
	inputChan chan readResult
	doneChan  chan struct{}
}

func newSessionReader(rlInput io.Reader) *sessionReader {
	sr := &sessionReader{
		doneChan:  make(chan struct{}),
	}
	
	if cr, ok := rlInput.(interface{ GetInputChan() chan byte }); ok {
		sr.chanInput = cr.GetInputChan()
		return sr
	}

	sr.inputChan = make(chan readResult, 100)
	go func() {
		var readBuf [1024]byte
		for {
			n, err := rlInput.Read(readBuf[:])
			if n > 0 {
				data := make([]byte, n)
				copy(data, readBuf[:n])
				select {
				case <-sr.doneChan:
					return
				case sr.inputChan <- readResult{data: data, err: err}:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return sr
}

func (sr *sessionReader) Close() {
	if sr.doneChan != nil {
		select {
		case <-sr.doneChan:
		default:
			close(sr.doneChan)
		}
	}
}

func (sr *sessionReader) ReadKeyOrResize(sigChan chan os.Signal) ([]byte, bool, error) {
	if sr.chanInput != nil {
		select {
		case <-sigChan:
			return nil, true, nil
		case b, ok := <-sr.chanInput:
			if !ok {
				return nil, false, io.EOF
			}
			buf := []byte{b}
			if b == 27 { // Escape character, wait for arrow keys
				for {
					select {
					case next, ok := <-sr.chanInput:
						if ok {
							buf = append(buf, next)
						} else {
							return buf, false, nil
						}
					case <-time.After(15 * time.Millisecond):
						return buf, false, nil
					}
				}
			}
			// Drain other buffered bytes
			for {
				select {
				case next, ok := <-sr.chanInput:
					if ok {
						buf = append(buf, next)
					} else {
						return buf, false, nil
					}
				default:
					return buf, false, nil
				}
			}
		}
	}

	select {
	case <-sigChan:
		return nil, true, nil
	case res, ok := <-sr.inputChan:
		if !ok {
			return nil, false, io.EOF
		}
		return res.data, false, res.err
	}
}

func (sr *sessionReader) ReadLine(rlOutput io.Writer) (string, error) {
	var line strings.Builder
	if sr.chanInput != nil {
		for {
			select {
			case b, ok := <-sr.chanInput:
				if !ok {
					return "", fmt.Errorf("read error")
				}
				if b == '\r' || b == '\n' {
					fmt.Fprint(rlOutput, "\r\n")
					return line.String(), nil
				}
				if b == 127 || b == 8 {
					if line.Len() > 0 {
						s := line.String()
						runes := []rune(s)
						if len(runes) > 0 {
							truncated := string(runes[:len(runes)-1])
							line.Reset()
							line.WriteString(truncated)
							fmt.Fprint(rlOutput, "\b\x1b[K")
						}
					}
					continue
				}
				if b == 3 || b == 4 {
					return "", fmt.Errorf("cancelled")
				}
				if b >= 32 {
					line.WriteByte(b)
					fmt.Fprint(rlOutput, string(b))
				}
			}
		}
	}

	for {
		res, ok := <-sr.inputChan
		if !ok || res.err != nil {
			return "", fmt.Errorf("read error")
		}
		for _, b := range res.data {
			if b == '\r' || b == '\n' {
				fmt.Fprint(rlOutput, "\r\n")
				return line.String(), nil
			}
			if b == 127 || b == 8 {
				if line.Len() > 0 {
					s := line.String()
					runes := []rune(s)
					if len(runes) > 0 {
						truncated := string(runes[:len(runes)-1])
						line.Reset()
						line.WriteString(truncated)
						fmt.Fprint(rlOutput, "\b\x1b[K")
					}
				}
				continue
			}
			if b == 3 || b == 4 {
				return "", fmt.Errorf("cancelled")
			}
			if b >= 32 {
				line.WriteByte(b)
				fmt.Fprint(rlOutput, string(b))
			}
		}
	}
}