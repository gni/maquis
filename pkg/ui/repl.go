package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bidouille/pkg/agent"
	"bidouille/pkg/agent/tool"
	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui/style"
	"golang.org/x/term"
)

func autoCompleteCallback(line string, pos int, key rune, a *agent.Agent) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

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

	if strings.HasPrefix(wordToComplete, "/") {
		for _, c := range candidates {
			if strings.HasPrefix(c, line[:pos]) {
				matches = append(matches, c)
			}
		}
	}

	// Sub-argument autocomplete logic
	if len(matches) == 0 {
		if strings.HasPrefix(prefixToPos, "/config ") {
			isSet := strings.HasPrefix(prefixToPos, "/config set ")
			var configCandidates []string
			configKeys := []string{
				"endpoint", "model", "temperature", "auto_approve", "show_thinking",
				"collapse_results", "show_tokens", "theme", "context_limit", "steps",
				"direct_commands", "cert_file", "key_file", "skip_verify", "reasoning_effort",
				"before_tool_hook", "after_tool_hook",
			}
			if !isSet {
				configCandidates = append(configCandidates, "show", "set")
			}
			configCandidates = append(configCandidates, configKeys...)

			var filterPrefix string
			if isSet {
				filterPrefix = strings.TrimPrefix(prefixToPos, "/config set ")
			} else {
				filterPrefix = strings.TrimPrefix(prefixToPos, "/config ")
			}

			for _, c := range configCandidates {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c != "show" && c != "set" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/session ") {
			sessionSubcommands := []string{"list", "new", "load", "branch"}
			filterPrefix := strings.TrimPrefix(prefixToPos, "/session ")
			for _, c := range sessionSubcommands {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c == "branch" || c == "load" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/skills ") {
			skillsSubcommands := []string{"load"}
			filterPrefix := strings.TrimPrefix(prefixToPos, "/skills ")
			for _, c := range skillsSubcommands {
				if strings.HasPrefix(c, filterPrefix) {
					match := c
					if c == "load" {
						match += " "
					}
					matches = append(matches, match)
				}
			}
		} else if strings.HasPrefix(prefixToPos, "/skills load ") && a != nil {
			filterPrefix := strings.TrimPrefix(prefixToPos, "/skills load ")
			for _, s := range a.ActiveSkills {
				if strings.HasPrefix(s.Name, filterPrefix) {
					matches = append(matches, s.Name)
				}
			}
		}
	}

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

func (cw crnlWriter) Unwrap() io.Writer {
	return cw.w
}

type keyInterceptorReader struct {
	r                io.Reader
	agent            *agent.Agent
	theme            style.UITheme
	w                io.Writer
	rl               *term.Terminal
	currentInputLine string
	messages         *[]db.Message
	pasteRemaining   []byte
	ctrlCInterrupted bool
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

		var cleanedBytes []byte
		for _, b := range pasteBytes {
			if b == '\n' || b == '\r' {
				cleanedBytes = append(cleanedBytes, []byte("↵")...)
			} else {
				cleanedBytes = append(cleanedBytes, b)
			}
		}

		nCopied := copy(p, cleanedBytes)
		if nCopied < len(cleanedBytes) {
			ki.pasteRemaining = cleanedBytes[nCopied:]
		}

		return nCopied, nil
	}

	writeIdx := 0
	for i := 0; i < n; i++ {
		b := p[i]
		if b == 3 { // Ctrl+C
			ki.ctrlCInterrupted = true
			p[writeIdx] = '\n'
			writeIdx++
		} else if b == 20 || b == 18 || b == 15 { // Ctrl+T, Ctrl+R, or Ctrl+O
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
				SetCollapseStatus(ki.agent.Config.CollapseResults)
			}

			if ki.rl != nil {
				redrawScreen(ki.w, ki.agent, ki, ki.rl)
			}
		} else {
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}

func RunREPL(a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {
	currentSessionID := initialSessionID
	if currentSessionID == "" {
		currentSessionID = db.NewUUID()
	}

	var exitMessage string
	var messages []db.Message
	if dbHistory, err := db.LoadMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
		exitMessage = fmt.Sprintf("loaded past session %s (%d messages)", currentSessionID, len(messages))
	} else {
		messages = []db.Message{
			{Role: "system", Content: a.GetSystemPrompt()},
		}
		if initialSessionID != "" {
			exitMessage = fmt.Sprintf("initialized brand new session %s", currentSessionID)
		} else {
			exitMessage = fmt.Sprintf("started new session %s", currentSessionID)
		}
	}

	initialPromptTokens, initialCompletionTokens := a.GetGlobalTokens(messages, allowedTools)

	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	SetScrollRegionOffset(2)
	InitStatusBar(os.Stderr)
	defer ShutdownStatusBar(os.Stderr)

	_, height := getTerminalSize()
	ppWriter := agent.NewPromptPreservingWriter(os.Stderr, height)

	// Print MCP startup errors if any
	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(ppWriter, a.McpStartErrors, theme)
	}

	PrintBanner(ppWriter, a.Config)

	SetCollapseStatus(a.Config.CollapseResults)
	UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, activeTasks)
	DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
	DrawStatusBar(os.Stderr, theme)

	fd := int(os.Stdin.Fd())

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

	// Set initial terminal size
	if w, h, err := term.GetSize(fd); err == nil {
		rl.SetSize(w, h)
	}

	// Setup SIGWINCH listener for dynamic terminal resizing with 100ms debouncing
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		var debounceTimer *time.Timer
		for range sigChan {
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
				if w, h, err := term.GetSize(fd); err == nil {
					rl.SetSize(w, h)
					redrawScreen(os.Stderr, a, kiReader, rl)
				}
			})
		}
	}()
	defer func() {
		signal.Stop(sigChan)
	}()

	rl.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		kiReader.currentInputLine = line
		return autoCompleteCallback(line, pos, key, a)
	}

	for {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		promptStr := promptStyle.Render("> ")
		rl.SetPrompt(promptStr)
		_, height := getTerminalSize()
		if height > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K", height-2)
		}

		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Printf("Error setting terminal raw mode: %v\n", err)
			os.Exit(1)
		}

		line, err := rl.ReadLine()
		term.Restore(fd, oldState)

		if kiReader.ctrlCInterrupted {
			kiReader.ctrlCInterrupted = false
			fmt.Fprint(os.Stderr, "^C\n")
			activeTasks := 0
			for _, t := range a.ListTasks() {
				if t.Status == "running" {
					activeTasks++
				}
			}
			pTok, cTok := a.GetGlobalTokens(messages, allowedTools)
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		if err != nil {
			break
		}

		kiReader.currentInputLine = ""
		line = strings.ReplaceAll(line, "↵", "\n")

		if strings.TrimSpace(line) == "" {
			activeTasks := 0
			for _, t := range a.ListTasks() {
				if t.Status == "running" {
					activeTasks++
				}
			}
			pTok, cTok := a.GetGlobalTokens(messages, allowedTools)
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

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
					fmt.Fprintf(os.Stderr, "changed directory to: %s\n", pwd)
				}
				contextMsg := fmt.Sprintf("[user manually changed working directory to: `%s`]", target)
				messages = append(messages, db.Message{Role: "user", Content: contextMsg})
				_ = db.SaveMessage(currentSessionID, messages[len(messages)-1])
				DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
				DrawStatusBar(os.Stderr, theme)
				continue
			}

			fmt.Fprintf(os.Stderr, "executing: %s\n", cmdStr)
			ShutdownStatusBar(os.Stderr)

			cmd := exec.Command("bash", "-c", cmdStr)
			cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
			cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
			cmd.Stdin = os.Stdin
			err := cmd.Run()

			InitStatusBar(os.Stderr)
			DrawStatusBar(os.Stderr, theme)

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
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		if handled, quit := HandleSlashCommand(a, line, &messages, allowedTools, &theme, os.Stderr, &currentSessionID, rl.History); handled {
			if quit {
				break
			}
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		a.RunAgentLoop(os.Stderr, &messages, line, allowedTools, theme, false, currentSessionID)
		DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
		DrawStatusBar(os.Stderr, theme)
	}

	var finalStatus string
	if currentSessionID == initialSessionID {
		finalStatus = exitMessage
	} else {
		if len(messages) > 1 {
			finalStatus = fmt.Sprintf("session %s", currentSessionID)
		} else {
			finalStatus = fmt.Sprintf("initialized brand new session %s", currentSessionID)
		}
	}
	fmt.Fprintf(os.Stderr, "goodbye! %s.\n", finalStatus)
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
		"ls":    true,
		"pwd":   true,
		"git":   true,
		"cat":   true,
		"cd":    true,
		"mkdir": true,
		"touch": true,
		"rm":    true,
		"mv":    true,
		"cp":    true,
		"grep":  true,
		"find":  true,
		"chmod": true,
		"make":  true,
	}

	if directCommands[firstWord] {
		return true, line
	}

	return false, ""
}

func redrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	activeTheme := style.GetTheme(a.Config.Theme)
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render("> ")

	if rl != nil {
		rl.SetPrompt(promptStr)
	}

	cw := crnlWriter{w: w}

	// Reset lastH to 0 so DrawStatusBar doesn't perform clamped clear on old height
	lastH = 0

	// 1. Clear screen
	fmt.Fprint(cw, "\x1b[H\x1b[2J")

	// Buffer everything that goes into the scroll region (rows 1..height-4)
	var buf bytes.Buffer
	cwBuf := crnlWriter{w: &buf}

	// 2. Render startup errors if any
	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, a.McpStartErrors, activeTheme)
	}

	// 3. Render banner and history
	PrintBanner(cwBuf, a.Config)
	if kiReader.messages != nil {
		PrintSessionHistory(cwBuf, *kiReader.messages, activeTheme, a.Config)
	}

	// Calculate terminal size
	_, height := getTerminalSize()
	scrollBottom := height - 4
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	content := buf.String()
	linesCount := strings.Count(content, "\n")

	// If content is shorter than the scroll region, print padding newlines first
	// to push history down snugly right above the prompt separator
	if linesCount < scrollBottom {
		padding := scrollBottom - linesCount
		fmt.Fprint(cw, strings.Repeat("\n", padding))
	}

	// Print the buffered content
	fmt.Fprint(cw, content)

	// 4. Draw prompt separator
	DrawStaticPromptSeparator(cw, a.Config.ShowThinking, a.Config.ReasoningEffort, activeTheme)

	// 5. Update status bar metrics and redraw status bar
	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	pTok, cTok := 0, 0
	if kiReader.messages != nil {
		pTok, cTok = a.GetGlobalTokens(*kiReader.messages, nil)
	}

	UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks)
	DrawStatusBar(cw, activeTheme)

	// 6. Draw input line and place cursor
	fmt.Fprintf(cw, "\x1b[%d;1H\x1b[2K", height-2)
	fmt.Fprint(cw, promptStr)
	fmt.Fprint(cw, kiReader.currentInputLine)
	fmt.Fprintf(cw, "\x1b[%d;%dH", height-2, 3+len(kiReader.currentInputLine))
}
