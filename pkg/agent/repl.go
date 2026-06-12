package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"bidouille/pkg/agent/tool"
	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
	"bidouille/pkg/ui/style"
	"golang.org/x/term"
)

func autoCompleteCallback(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

	// Identify the word being completed
	prefixToPos := line[:pos]
	lastSpaceIdx := strings.LastIndex(prefixToPos, " ")
	wordStartIdx := lastSpaceIdx + 1
	wordToComplete := prefixToPos[wordStartIdx:]

	candidates := []string{
		"/goal ",
		"/schedule ",
		"/config ",
		"/config show",
		"/config set ",
		"/rewind",
		"/skills",
		"/skills load ",
		"/session ",
		"/session list",
		"/session new",
		"/session load",
		"/session branch ",
		"/help",
		"/commands",
		"/exit",
		"/quit",
		"/multiline",
		"/paste",
		"/mcp",
		"/toggle",
		"/collapse",
		"/expand",
	}

	var matches []string

	// Check if the word matches any slash commands first (only if it starts with /)
	if strings.HasPrefix(wordToComplete, "/") {
		for _, c := range candidates {
			if strings.HasPrefix(c, line[:pos]) {
				matches = append(matches, c)
			}
		}
	}

	// If no command matches, check filesystem paths
	if len(matches) == 0 {
		dirPart, filePart := filepath.Split(wordToComplete)
		searchDir := dirPart
		if searchDir == "" {
			searchDir = "."
		}

		entries, err := os.ReadDir(searchDir)
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, filePart) {
					fullName := dirPart + name
					if entry.IsDir() {
						fullName += "/"
					}
					matches = append(matches, fullName)
				}
			}
		}
	}

	if len(matches) == 0 {
		return line, pos, false
	}

	if len(matches) == 1 {
		completedLine := line[:wordStartIdx] + matches[0] + line[pos:]
		newPos := wordStartIdx + len(matches[0])
		return completedLine, newPos, true
	}

	commonPrefix := matches[0]
	for _, m := range matches[1:] {
		for i := 0; i < len(commonPrefix) && i < len(m); i++ {
			if commonPrefix[i] != m[i] {
				commonPrefix = commonPrefix[:i]
				break
			}
		}
		if len(commonPrefix) > len(m) {
			commonPrefix = m
		}
	}

	if len(commonPrefix) > len(wordToComplete) {
		completedLine := line[:wordStartIdx] + commonPrefix + line[pos:]
		newPos := wordStartIdx + len(commonPrefix)
		return completedLine, newPos, true
	}

	return line, pos, false
}

type customHistory struct {
	entries []string
}

func Deduplicate(entries []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !seen[entry] {
			seen[entry] = true
			unique = append([]string{entry}, unique...)
		}
	}
	return unique
}

func (h *customHistory) Add(entry string) {
	if entry == "" {
		return
	}
	h.entries = append(h.entries, entry)
	h.entries = Deduplicate(h.entries)
}

func (h *customHistory) Len() int {
	return len(h.entries)
}

func (h *customHistory) At(idx int) string {
	if idx < 0 || idx >= len(h.entries) {
		return ""
	}
	// Return from the end (newest first: idx 0 is the last entry in h.entries)
	return h.entries[len(h.entries)-1-idx]
}

type crnlWriter struct {
	w io.Writer
}

func (cw crnlWriter) Write(p []byte) (int, error) {
	var buf []byte
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' {
			if i == 0 || p[i-1] != '\r' {
				buf = append(buf, '\r')
			}
		}
		buf = append(buf, p[i])
	}
	_, err := cw.w.Write(buf)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

type keyInterceptorReader struct {
	r                io.Reader
	agent            *Agent
	theme            ui.UITheme
	w                io.Writer
	rl               *term.Terminal
	currentInputLine string
	messages         *[]db.Message
	pasteRemaining   []byte
}

func (ki *keyInterceptorReader) Write(p []byte) (int, error) {
	if w, ok := ki.r.(io.Writer); ok {
		return w.Write(p)
	}
	return os.Stdout.Write(p)
}

func (ki *keyInterceptorReader) Read(p []byte) (int, error) {
	if len(ki.pasteRemaining) > 0 {
		n := copy(p, ki.pasteRemaining)
		ki.pasteRemaining = ki.pasteRemaining[n:]
		return n, nil
	}

	n, err := ki.r.Read(p)
	if err != nil {
		return n, err
	}

	// Detect multiline paste (multiple characters read at once containing newline)
	hasNewlines := false
	for i := 0; i < n; i++ {
		if p[i] == '\n' || p[i] == '\r' {
			hasNewlines = true
			break
		}
	}
	isMultilinePaste := (n > 1 && hasNewlines)

	if isMultilinePaste {
		var pasteBytes []byte
		pasteBytes = append(pasteBytes, p[:n]...)

		// Read all remaining buffered bytes from stdin non-blockingly
		if f, ok := ki.r.(*os.File); ok {
			fd := int(f.Fd())
			_ = syscall.SetNonblock(fd, true)
			defer syscall.SetNonblock(fd, false)

			tempBuf := make([]byte, 4096)
			for {
				n2, err2 := ki.r.Read(tempBuf)
				if err2 != nil || n2 == 0 {
					break
				}
				pasteBytes = append(pasteBytes, tempBuf[:n2]...)
			}
		}

		// Replace newlines/carriage returns with ↵ symbol for visual representation and to prevent instant submission
		var cleanedBytes []byte
		for _, b := range pasteBytes {
			if b == '\n' || b == '\r' {
				cleanedBytes = append(cleanedBytes, []byte("↵")...)
			} else {
				cleanedBytes = append(cleanedBytes, b)
			}
		}

		// Copy to p and buffer the rest
		nCopied := copy(p, cleanedBytes)
		if nCopied < len(cleanedBytes) {
			ki.pasteRemaining = cleanedBytes[nCopied:]
		}

		return nCopied, nil
	}

	writeIdx := 0
	for i := 0; i < n; i++ {
		b := p[i]
		if b == 20 || b == 18 || b == 15 { // Ctrl+T, Ctrl+R, or Ctrl+O
			if b == 20 { // Ctrl+T
				ki.agent.Config.ShowThinking = !ki.agent.Config.ShowThinking
				_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
			} else if b == 18 { // Ctrl+R
				nextEffort := "low"
				switch strings.ToLower(ki.agent.Config.ReasoningEffort) {
				case "low":
					nextEffort = "medium"
				case "medium":
					nextEffort = "high"
				case "high":
					nextEffort = "max"
				case "max":
					nextEffort = "low"
				default:
					nextEffort = "low"
				}
				ki.agent.Config.ReasoningEffort = nextEffort
				_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
			} else if b == 15 { // Ctrl+O
				ki.agent.Config.CollapseResults = !ki.agent.Config.CollapseResults
				_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
				ui.SetCollapseStatus(ki.agent.Config.CollapseResults)
			}

			if ki.rl != nil {
				activeTheme := ui.GetTheme(ki.agent.Config.Theme)
				promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
				promptStr := promptStyle.Render("> ")
				ki.rl.SetPrompt(promptStr)

				cw := crnlWriter{w: ki.w}
				// Clear screen and scrollback
				fmt.Fprint(cw, "\x1b[H\x1b[2J")
				// Print banner
				ui.PrintBanner(cw, ki.agent.Config)
				// Print history with updated settings
				if ki.messages != nil {
					printSessionHistory(cw, *ki.messages, activeTheme, ki.agent.Config)
				}
				// Print prompt separator
				ki.agent.PrintPromptSeparator(cw, activeTheme)
				// Draw status bar
				ui.DrawStatusBar(cw, activeTheme)
				// Restore prompt and typed input
				fmt.Fprintf(cw, "\r\x1b[K%s%s", promptStr, ki.currentInputLine)
			}
			// Skip returning this byte to term.Terminal
		} else {
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}

func (a *Agent) RunREPL(allowedTools []string, theme ui.UITheme, initialSessionID string) {
	currentSessionID := initialSessionID
	if currentSessionID == "" {
		currentSessionID = db.NewUUID()
	}

	var exitMessage string
	var messages []db.Message
	if dbHistory, err := db.LoadMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
		exitMessage = fmt.Sprintf("Loaded past session %s (%d messages)", currentSessionID, len(messages))
	} else {
		messages = []db.Message{
			{Role: "system", Content: a.GetSystemPrompt()},
		}
		if initialSessionID != "" {
			exitMessage = fmt.Sprintf("Initialized brand new session %s", currentSessionID)
		} else {
			exitMessage = fmt.Sprintf("Started new session %s", currentSessionID)
		}
	}

	ui.PrintBanner(os.Stderr, a.Config)
	a.PrintPromptSeparator(os.Stderr, theme)

	// Initialize status bar with estimated session tokens
	initialPromptTokens, initialCompletionTokens := a.GetGlobalTokens(messages, allowedTools)

	ui.InitStatusBar(os.Stderr)
	defer ui.ShutdownStatusBar(os.Stderr)
	ui.SetCollapseStatus(a.Config.CollapseResults)
	ui.UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0)
	ui.DrawStatusBar(os.Stderr, theme)

	fd := int(os.Stdin.Fd())

	// Load command history
	historyLines, _ := db.GetUserHistory()
	hist := &customHistory{}
	for _, hLine := range historyLines {
		hist.Add(strings.TrimSpace(strings.ReplaceAll(hLine, "\n", " ")))
	}

	kiReader := &keyInterceptorReader{
		r:        os.Stdin,
		agent:    a,
		theme:    theme,
		w:        os.Stderr,
		messages: &messages,
	}
	rl := term.NewTerminal(kiReader, "")
	kiReader.rl = rl
	rl.History = hist
	rl.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		kiReader.currentInputLine = line
		return autoCompleteCallback(line, pos, key)
	}

	for {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		promptStr := promptStyle.Render("> ")
		rl.SetPrompt(promptStr)

		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Printf("Error setting terminal raw mode: %v\n", err)
			os.Exit(1)
		}

		line, err := rl.ReadLine()
		term.Restore(fd, oldState)

		if err != nil {
			break
		}

		kiReader.currentInputLine = ""

		// Restore original newlines for pasted multiline content
		line = strings.ReplaceAll(line, "↵", "\n")

		if strings.TrimSpace(line) == "" {
			ui.DrawStatusBar(os.Stderr, theme)
			continue
		}

		hist.Add(line)

		fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("──────────────────────────────────────────────────────────"))

		isCmd, cmdStr := parseManualCommand(line, a.Config.DirectCommands)
		if isCmd {
			if strings.HasPrefix(cmdStr, "cd ") || cmdStr == "cd" {
				target := strings.TrimSpace(strings.TrimPrefix(cmdStr, "cd"))
				if target == "" {
					home, err := os.UserHomeDir()
					if err == nil {
						target = home
					}
				}
				err := os.Chdir(target)
				if err != nil {
					fmt.Fprintf(os.Stderr, "cd: %v\n", err)
				} else {
					pwd, _ := os.Getwd()
					fmt.Fprintf(os.Stderr, "Changed directory to: %s\n", pwd)
				}
				contextMsg := fmt.Sprintf("[User manually changed working directory to: `%s`]", target)
				messages = append(messages, db.Message{Role: "user", Content: contextMsg})
				_ = db.SaveMessage(currentSessionID, messages[len(messages)-1])
				a.PrintPromptSeparator(os.Stderr, theme)
				ui.DrawStatusBar(os.Stderr, theme)
				continue
			}

			fmt.Fprintf(os.Stderr, "Executing: %s\n", cmdStr)

			// Temporarily disable status bar during manual shell commands
			ui.ShutdownStatusBar(os.Stderr)

			cmd := exec.Command("bash", "-c", cmdStr)
			cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
			cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
			cmd.Stdin = os.Stdin
			err := cmd.Run()

			// Re-enable status bar after manual command execution
			ui.InitStatusBar(os.Stderr)
			ui.DrawStatusBar(os.Stderr, theme)

			output := tool.SanitizeUTF8(stdout.Bytes())
			errOutput := tool.SanitizeUTF8(stderr.Bytes())

			if err != nil {
				fmt.Fprintf(os.Stderr, "Command failed: %v\n", err)
			}

			combined := ""
			if output != "" {
				combined += fmt.Sprintf("STDOUT:\n%s\n", output)
			}
			if errOutput != "" {
				combined += fmt.Sprintf("STDERR:\n%s\n", errOutput)
			}
			if err != nil {
				combined += fmt.Sprintf("ERROR:\n%v\n", err)
			}
			if combined == "" {
				combined = "(command completed with no output)"
			}

			contextMsg := fmt.Sprintf("[User manually executed local shell command: `%s`]\n%s", cmdStr, combined)
			messages = append(messages, db.Message{Role: "user", Content: contextMsg})
			_ = db.SaveMessage(currentSessionID, messages[len(messages)-1])

			successStyle := style.NewStyle().Foreground(theme.Success).Italic(true)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, successStyle.Render("Command output appended to conversation context."))
			a.PrintPromptSeparator(os.Stderr, theme)
			ui.DrawStatusBar(os.Stderr, theme)
			continue
		}

		if handled, quit := a.HandleSlashCommand(line, &messages, allowedTools, &theme, os.Stderr, &currentSessionID, rl.History); handled {
			if quit {
				break
			}
			a.PrintPromptSeparator(os.Stderr, theme)
			ui.DrawStatusBar(os.Stderr, theme)
			continue
		}

		a.RunAgentLoop(os.Stderr, &messages, line, allowedTools, theme, false, currentSessionID)
		a.PrintPromptSeparator(os.Stderr, theme)
		ui.DrawStatusBar(os.Stderr, theme)
	}

	var finalStatus string
	if currentSessionID == initialSessionID {
		finalStatus = exitMessage
	} else {
		if len(messages) > 1 {
			finalStatus = fmt.Sprintf("Session %s", currentSessionID)
		} else {
			finalStatus = fmt.Sprintf("Initialized brand new session %s", currentSessionID)
		}
	}
	fmt.Fprintf(os.Stderr, "Goodbye! %s.\n", finalStatus)
}

func parseManualCommand(line string, enabled bool) (bool, string) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "!") {
		return true, strings.TrimSpace(strings.TrimPrefix(line, "!"))
	}

	if !enabled {
		return false, ""
	}

	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, ""
	}

	firstWord := parts[0]
	directCommands := map[string]bool{
		"ls":  true,
		"pwd": true,
		"git": true,
		"cat": true,
		"cd":  true,
	}

	if directCommands[firstWord] {
		return true, line
	}

	return false, ""
}

func (a *Agent) PrintPromptSeparator(w io.Writer, theme ui.UITheme) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	thinkingText := "off"
	if a.Config.ShowThinking {
		thinkingText = a.Config.ReasoningEffort
	}
	statusPart := fmt.Sprintf("[reasoning:%s]", thinkingText)

	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}

	prefix := "─── Prompt "
	dashesCount := width - 11 - len(statusPart) - 1
	if dashesCount < 5 {
		dashesCount = 5
	}
	dashes := strings.Repeat("─", dashesCount)
	fmt.Fprintf(w, "%s%s\n", borderStyle.Render(prefix+dashes), statusStyle.Render(statusPart))
}

func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	// Find the last assistant message
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	globalPrompt := 0
	globalCompletion := 0

	// Sum all completion tokens
	for _, m := range messages {
		if m.Role == "assistant" {
			if m.CompletionTokens > 0 {
				globalCompletion += m.CompletionTokens
			} else {
				globalCompletion += (len(m.Content) + len(m.ReasoningContent)) / 4
			}
		}
	}

	tools := a.Registry.GetAvailableTools(allowedTools)
	var toolsChars int
	if len(tools) > 0 {
		if toolsData, err := json.Marshal(tools); err == nil {
			toolsChars = len(toolsData)
		}
	}
	toolsEst := toolsChars / 4

	if lastAssistantIdx != -1 {
		// We have an assistant message.
		// Start with the prompt tokens of that turn.
		if messages[lastAssistantIdx].PromptTokens > 0 {
			globalPrompt = messages[lastAssistantIdx].PromptTokens
		} else {
			// Fallback if not stored
			for i := 0; i <= lastAssistantIdx; i++ {
				m := messages[i]
				if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
					globalPrompt += len(m.Content) / 4
				}
			}
			globalPrompt += toolsEst
		}

		// Add estimation for any messages after the last assistant message
		for i := lastAssistantIdx + 1; i < len(messages); i++ {
			m := messages[i]
			if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
				globalPrompt += len(m.Content) / 4
			}
		}
	} else {
		// No assistant messages yet. Estimate everything.
		for _, m := range messages {
			if m.Role == "user" || m.Role == "system" || m.Role == "tool" {
				globalPrompt += len(m.Content) / 4
			}
		}
		if globalPrompt == 0 {
			globalPrompt = len(a.GetSystemPrompt()) / 4
		}
		globalPrompt += toolsEst
	}

	return globalPrompt, globalCompletion
}
