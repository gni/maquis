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

	"maquis/pkg/agent"
	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
	"golang.org/x/term"
)

var pasteLinesOffset int
var activeInputReader io.Reader

func autoCompleteCallback(line string, pos int, key rune, a *agent.Agent) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

	prefixToPos := line[:pos]
	lastSpaceIdx := strings.LastIndex(prefixToPos, " ")
	wordStartIdx := lastSpaceIdx + 1
	wordToComplete := prefixToPos[wordStartIdx:]

	candidates := []string{
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
		"/session clear",
		"/help",
		"/commands",
		"/exit",
		"/quit",
		"/mcp",
		"/toggle",
		"/collapse",
		"/expand",
		"/provider ",
		"/provider list",
		"/provider add ",
		"/provider select ",
		"/provider model ",
		"/provider remove ",
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
				"collapse_results", "show_tokens", "theme", "syntax_theme", "context_limit", "steps",
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
			sessionSubcommands := []string{"list", "new", "load", "branch", "clear"}
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
		} else if strings.HasPrefix(prefixToPos, "/provider ") {
			providerSubcommands := []string{"list", "add", "select", "use", "model", "remove", "delete"}
			filterPrefix := strings.TrimPrefix(prefixToPos, "/provider ")
			if strings.HasPrefix(filterPrefix, "select ") || strings.HasPrefix(filterPrefix, "use ") || strings.HasPrefix(filterPrefix, "remove ") || strings.HasPrefix(filterPrefix, "delete ") {
				var subCmd string
				if strings.HasPrefix(filterPrefix, "select ") {
					subCmd = "select "
				} else if strings.HasPrefix(filterPrefix, "use ") {
					subCmd = "use "
				} else if strings.HasPrefix(filterPrefix, "remove ") {
					subCmd = "remove "
				} else {
					subCmd = "delete "
				}
				provPrefix := strings.TrimPrefix(filterPrefix, subCmd)
				var providerKeys []string
				providerKeys = append(providerKeys, "default")
				if a != nil && a.Config != nil && a.Config.Providers != nil {
					for k := range a.Config.Providers {
						providerKeys = append(providerKeys, k)
					}
				}
				for _, pk := range providerKeys {
					if strings.HasPrefix(pk, provPrefix) {
						matches = append(matches, subCmd + pk)
					}
				}
			} else {
				for _, c := range providerSubcommands {
					if strings.HasPrefix(c, filterPrefix) {
						match := c
						if c == "add" || c == "select" || c == "use" || c == "model" || c == "remove" || c == "delete" {
							match += " "
						}
						matches = append(matches, match)
					}
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
	pastedText       string
	ctrlCInterrupted bool
	mam              *agent.MultiAgentManager
	inputChan        chan byte
	injectChan       chan byte
}

func stylePrompt(p []byte, prefix string, styledPrefix string) []byte {
	if bytes.HasPrefix(p, []byte(prefix)) {
		return append([]byte(styledPrefix), p[len(prefix):]...)
	}
	rPrefix := append([]byte{'\r'}, []byte(prefix)...)
	if bytes.HasPrefix(p, rPrefix) {
		return append(append([]byte{'\r'}, []byte(styledPrefix)...), p[len(rPrefix):]...)
	}
	rkPrefix := append([]byte("\r\x1b[K"), []byte(prefix)...)
	if bytes.HasPrefix(p, rkPrefix) {
		return append(append([]byte("\r\x1b[K"), []byte(styledPrefix)...), p[len(rkPrefix):]...)
	}
	r2kPrefix := append([]byte("\r\x1b[2K"), []byte(prefix)...)
	if bytes.HasPrefix(p, r2kPrefix) {
		return append(append([]byte("\r\x1b[2K"), []byte(styledPrefix)...), p[len(r2kPrefix):]...)
	}
	return p
}

func (ki *keyInterceptorReader) Write(p []byte) (int, error) {
	activeTheme := GetConfiguredTheme(ki.agent.Config)
	promptPrefix := "> "
	if ki.mam != nil && ki.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]> ", ki.mam.ActiveAgent.Name)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	p = stylePrompt(p, promptPrefix, promptStr)

	if bytes.Contains(p, []byte("\x1b[2J")) {
		ki.redrawLayout()
		cleaned := bytes.ReplaceAll(p, []byte("\x1b[H\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[2J\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[1;1H"), nil)
		var err error
		if len(cleaned) > 0 {
			_, err = ki.writeToTerminal(cleaned)
		}
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}

	if bytes.Contains(p, []byte("\x1b[H")) || bytes.Contains(p, []byte("\x1b[1;1H")) {
		cleaned := bytes.ReplaceAll(p, []byte("\x1b[2J\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[H\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[1;1H"), nil)
		var err error
		if len(cleaned) > 0 {
			_, err = ki.writeToTerminal(cleaned)
		}
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}

	return ki.writeToTerminal(p)
}

func (ki *keyInterceptorReader) writeToTerminal(p []byte) (int, error) {
	if ki.w != nil {
		return ki.w.Write(p)
	}
	if w, ok := ki.r.(io.Writer); ok {
		return w.Write(p)
	}
	return os.Stdout.Write(p)
}

func (ki *keyInterceptorReader) redrawLayout() {
	activeTheme := GetConfiguredTheme(ki.agent.Config)
	cw := crnlWriter{w: os.Stderr}

	// Reset lastH to 0 so DrawStatusBar doesn't perform clamped clear on old height
	lastH = 0

	// 1. Clear screen
	fmt.Fprint(cw, "\x1b[H\x1b[2J")

	// Buffer everything that goes into the scroll region (rows 1..height-4)
	var buf bytes.Buffer
	cwBuf := crnlWriter{w: &buf}

	// 2. Render startup errors if any
	if len(ki.agent.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, ki.agent.McpStartErrors, activeTheme)
	}

	// 3. Render banner and history
	PrintBanner(cwBuf, ki.agent)
	activeMessages := []db.Message{}
	if ki.messages != nil {
		activeMessages = *ki.messages
	}
	if ki.mam != nil && ki.mam.ActiveAgent != nil {
		ki.mam.ActiveAgent.HistoryMu.RLock()
		activeMessages = ki.mam.ActiveAgent.History
		ki.mam.ActiveAgent.HistoryMu.RUnlock()
	}
	if len(activeMessages) > 0 {
		PrintSessionHistory(cwBuf, activeMessages, activeTheme, ki.agent.Config)
	}

	// Calculate terminal size
	_, height := getTerminalSize()
	scrollBottom := height - 4 - pasteLinesOffset
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
	DrawStaticPromptSeparator(cw, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme)

	// 4b. Draw the last stats line
	stateMu.Lock()
	savedStats := lastStatsText
	stateMu.Unlock()
	DrawStaticStatsLine(cw, activeTheme, "", savedStats)

	// 5. Update status bar metrics and redraw status bar
	activeTasks := 0
	for _, t := range ki.agent.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	pTok, cTok := 0, 0
	activeMessagesForTokens := []db.Message{}
	if ki.messages != nil {
		activeMessagesForTokens = *ki.messages
	}
	if ki.mam != nil && ki.mam.ActiveAgent != nil {
		ki.mam.ActiveAgent.HistoryMu.RLock()
		activeMessagesForTokens = ki.mam.ActiveAgent.History
		ki.mam.ActiveAgent.HistoryMu.RUnlock()
	}
	pTok, cTok = ki.agent.GetGlobalTokens(activeMessagesForTokens, nil)

	UpdateStatus(ki.agent.Config.Model, pTok, cTok, 0, ki.agent.Config.ContextWindowLimit, false, 0, activeTasks, ki.agent.Config.ShowTokens)
	DrawStatusBar(cw, activeTheme)

	// Position cursor at prompt line for term.Terminal to draw the prompt
	fmt.Fprintf(cw, "\x1b[%d;1H\x1b[2K", height-2-pasteLinesOffset)
}

func (ki *keyInterceptorReader) Read(p []byte) (int, error) {
	if len(ki.pasteRemaining) > 0 {
		n := copy(p, ki.pasteRemaining)
		ki.pasteRemaining = ki.pasteRemaining[n:]
		return n, nil
	}

	var n int
	var err error
	if ki.inputChan == nil {
		n, err = ki.r.Read(p)
		if err != nil {
			return n, err
		}
	} else {
		var b byte
		select {
		case injected, ok := <-ki.injectChan:
			if !ok {
				return 0, io.EOF
			}
			b = injected
		case input, ok := <-ki.inputChan:
			if !ok {
				return 0, io.EOF
			}
			b = input
		}

		p[0] = b
		n = 1

		// Read any additional bytes. If we saw an Escape char, wait up to 15ms for the next byte to ensure we don't split Escape sequences.
		for n < len(p) {
			hasEscape := false
			for i := 0; i < n; i++ {
				if p[i] == 27 {
					hasEscape = true
					break
				}
			}

			if hasEscape {
				select {
				case input, ok := <-ki.inputChan:
					if ok {
						p[n] = input
						n++
					} else {
						goto done
					}
				case <-time.After(15 * time.Millisecond):
					goto done
				}
			} else {
				select {
				case input, ok := <-ki.inputChan:
					if ok {
						p[n] = input
						n++
					} else {
						break
					}
				default:
					goto done
				}
			}
		}
	done:
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

		// Read any remaining paste bytes from inputChan with a 10ms idle timeout
		if ki.inputChan != nil {
			timer := time.NewTimer(10 * time.Millisecond)
			defer timer.Stop()

			for {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(10 * time.Millisecond)

				select {
				case input, ok := <-ki.inputChan:
					if ok {
						pasteBytes = append(pasteBytes, input)
					} else {
						goto pasteDone
					}
				case <-timer.C:
					goto pasteDone
				}
			}
		pasteDone:
		}

		var normalized []byte
		for i := 0; i < len(pasteBytes); i++ {
			if pasteBytes[i] == '\r' {
				if i+1 < len(pasteBytes) && pasteBytes[i+1] == '\n' {
					i++
				}
				normalized = append(normalized, '\n')
			} else {
				normalized = append(normalized, pasteBytes[i])
			}
		}

		ki.pastedText = string(normalized)

		if ki.w != nil {
			pasteLinesOffset = bytes.Count(normalized, []byte("\n"))
			ki.redrawLayout()

			cw := crnlWriter{w: ki.w}
			_, _ = cw.Write(normalized)
		}

		return 0, nil
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

			// Inject Ctrl+L (12) to trigger a redraw via term.Terminal
			p[writeIdx] = 12
			writeIdx++
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

	var messages []db.Message
	if dbHistory, err := db.LoadMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
	} else {
		messages = []db.Message{
			{Role: "system", Content: a.GetSystemPrompt()},
		}
	}

	initialPromptTokens, initialCompletionTokens := a.GetGlobalTokens(messages, allowedTools)

	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	SetScrollRegionOffset(3)
	InitStatusBar(os.Stderr)
	defer ShutdownStatusBar(os.Stderr)
	defer fmt.Fprint(os.Stderr, "\x1b[?25h") // Ensure cursor is restored to visible when exiting

	_, height := getTerminalSize()
	ppWriter := agent.NewPromptPreservingWriter(os.Stderr, height)

	// Print MCP startup errors if any
	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(ppWriter, a.McpStartErrors, theme)
	}

	fd := int(os.Stdin.Fd())

	historyLines, _ := db.GetUserHistory()
	hist := &customHistory{}
	for _, hLine := range historyLines {
		hist.Add(strings.TrimSpace(strings.ReplaceAll(hLine, "\n", " ")))
	}

	mam := agent.NewMultiAgentManager(a, ppWriter, theme)
	_ = mam.LoadSavedAgents()

	kiReader := &keyInterceptorReader{
		r:          os.Stdin,
		agent:      a,
		theme:      theme,
		w:          os.Stderr,
		messages:   &messages,
		mam:        mam,
		inputChan:  make(chan byte, 1000),
		injectChan: make(chan byte, 100),
	}
	activeInputReader = kiReader
	defer func() { activeInputReader = nil }()

	// Start input reader goroutine
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := kiReader.r.Read(buf)
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
					close(kiReader.inputChan)
					return
				}
				// Sleep and retry for EAGAIN/EWOULDBLOCK errors when fd is in non-blocking mode
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for i := 0; i < n; i++ {
				kiReader.inputChan <- buf[i]
			}
		}
	}()

	rl := term.NewTerminal(kiReader, "")
	kiReader.rl = rl
	rl.History = hist

	// Set initial terminal size
	if w, h, err := term.GetSize(fd); err == nil {
		rl.SetSize(w, h)
	}

	PrintBanner(ppWriter, a)

	SetCollapseStatus(a.Config.CollapseResults)
	UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
	DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
	stateMu.Lock()
	savedStats := lastStatsText
	stateMu.Unlock()
	DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
	DrawStatusBar(os.Stderr, theme)

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
					select {
					case kiReader.injectChan <- 12:
					default:
					}
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
		promptPrefix := "> "
		if mam.ActiveAgent != nil {
			promptPrefix = fmt.Sprintf("[%s]> ", mam.ActiveAgent.Name)
		}
		ppWriter.SetPromptCol(1 + len(promptPrefix))
		rl.SetPrompt(promptPrefix)
		_, height := getTerminalSize()
		if height > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K", height-2-pasteLinesOffset)
		}

		// Show cursor when waiting for input
		fmt.Fprint(os.Stderr, "\x1b[?25h")
		ppWriter.SetCursorHidden(false)

		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Printf("Error setting terminal raw mode: %v\n", err)
			os.Exit(1)
		}

		line, err := rl.ReadLine()
		term.Restore(fd, oldState)

		if kiReader.pastedText != "" {
			line = line + kiReader.pastedText
			kiReader.pastedText = ""
			pasteLinesOffset = 0
			InitStatusBar(os.Stderr)
		}

		// Hide cursor immediately after reading input (keeps cursor hidden during generation)
		fmt.Fprint(os.Stderr, "\x1b[?25l")
		ppWriter.SetCursorHidden(true)

		// Redraw prompts immediately to correct any scrolling caused by pressing Enter
		DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
		stateMu.Lock()
		savedStats = lastStatsText
		stateMu.Unlock()
		DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
		DrawStatusBar(os.Stderr, theme)

		if kiReader.ctrlCInterrupted {
			kiReader.ctrlCInterrupted = false
			kiReader.pastedText = ""
			pasteLinesOffset = 0
			InitStatusBar(os.Stderr)
			fmt.Fprint(os.Stderr, "^C\n")
			activeTasks := 0
			for _, t := range a.ListTasks() {
				if t.Status == "running" {
					activeTasks++
				}
			}
			activeMessagesForTokens := messages
			if mam.ActiveAgent != nil {
				mam.ActiveAgent.HistoryMu.RLock()
				activeMessagesForTokens = mam.ActiveAgent.History
				mam.ActiveAgent.HistoryMu.RUnlock()
			}
			pTok, cTok := a.GetGlobalTokens(activeMessagesForTokens, allowedTools)
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			stateMu.Lock()
			savedStats = lastStatsText
			stateMu.Unlock()
			DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
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
			activeMessagesForTokens := messages
			if mam.ActiveAgent != nil {
				mam.ActiveAgent.HistoryMu.RLock()
				activeMessagesForTokens = mam.ActiveAgent.History
				mam.ActiveAgent.HistoryMu.RUnlock()
			}
			pTok, cTok := a.GetGlobalTokens(activeMessagesForTokens, allowedTools)
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			stateMu.Lock()
			savedStats = lastStatsText
			stateMu.Unlock()
			DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		// Raw separator removed to prevent screen scrolling

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
				redrawScreen(os.Stderr, a, kiReader, rl)
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
				fmt.Fprintf(os.Stderr, "command failed: %v\n", err)
			}

			combined := ""
			if output != "" {
				combined += fmt.Sprintf("stdout:\n%s\n", output)
			}
			if errOutput != "" {
				combined += fmt.Sprintf("stderr:\n%s\n", errOutput)
			}
			if err != nil {
				combined += fmt.Sprintf("error:\n%v\n", err)
			}
			if combined == "" {
				combined = "(command completed with no output)"
			}

			contextMsg := fmt.Sprintf("[user manually executed local shell command: `%s`]\n%s", cmdStr, combined)
			messages = append(messages, db.Message{Role: "user", Content: contextMsg})
			_ = db.SaveMessage(currentSessionID, messages[len(messages)-1])

			successStyle := style.NewStyle().Foreground(theme.Success).Italic(true)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, successStyle.Render("command output appended to conversation context."))
			redrawScreen(os.Stderr, a, kiReader, rl)
			continue
		}

		trimmedLine := strings.TrimSpace(line)
		isHelpCmd := trimmedLine == "help" || trimmedLine == "h" || trimmedLine == "?" || trimmedLine == "/help" || trimmedLine == "/commands" || trimmedLine == "/h" || trimmedLine == "/?"
		isSlashCmd := strings.HasPrefix(trimmedLine, "/") || isHelpCmd

		var handled, quit bool
		if isSlashCmd {
			prevActiveAgent := ""
			if mam.ActiveAgent != nil {
				prevActiveAgent = mam.ActiveAgent.Name
			}
			prevSessionID := currentSessionID

			ShutdownStatusBar(os.Stderr)
			cw := crnlWriter{w: os.Stderr}
			handled, quit = HandleSlashCommand(a, line, &messages, allowedTools, &theme, cw, &currentSessionID, rl.History, mam, kiReader)
			InitStatusBar(os.Stderr)

			if handled && !quit {
				currActiveAgent := ""
				if mam.ActiveAgent != nil {
					currActiveAgent = mam.ActiveAgent.Name
				}
				parts := strings.Fields(strings.TrimSpace(line))
				cmdName := ""
				if len(parts) > 0 {
					cmdName = parts[0]
				}
				needsRedraw := currActiveAgent != prevActiveAgent ||
					currentSessionID != prevSessionID ||
					cmdName == "/collapse" || cmdName == "/expand" || cmdName == "/toggle" ||
					cmdName == "/rewind" ||
					(cmdName == "/config" && len(parts) == 1) ||
					(cmdName == "/session" && (len(parts) == 1 || (len(parts) > 1 && parts[1] == "explorer"))) ||
					(cmdName == "/agent" && len(parts) == 1)

				if needsRedraw {
					redrawScreen(os.Stderr, a, kiReader, rl)
					continue
				}
			}
		}

		if handled {
			if quit {
				break
			}
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			stateMu.Lock()
			savedStats = lastStatsText
			stateMu.Unlock()
			DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		if mam.ActiveAgent != nil {
			// Redraw status bar and static prompt separator before starting subagent execution
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			stateMu.Lock()
			savedStats = lastStatsText
			stateMu.Unlock()
			DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
			DrawStatusBar(os.Stderr, theme)

			// Print the prompt separator and user prompt inside the scroll region so they scroll up
			borderStyle := style.NewStyle().Foreground(theme.Border)
			statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			thinkingText := "off"
			if a.Config.ShowThinking {
				thinkingText = a.Config.ReasoningEffort
			}
			statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
			prefix := "─── prompt "
			width, _ := getTerminalSize()
			statusLen := len(stripAnsi(statusPart))
			prefixLen := len(prefix)
			dashesCount := width - prefixLen - statusLen - 2
			if dashesCount < 3 {
				dashesCount = 3
			}
			dashes := strings.Repeat("─", dashesCount)
			fmt.Fprintf(ppWriter, "%s%s\n", borderStyle.Render(prefix+dashes), statusStyle.Render(statusPart))

			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(ppWriter, "%s%s\n", promptStyle.Render(promptPrefix), line)

			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(ppWriter, divider)

			err := mam.SendMessage(mam.ActiveAgent.Name, line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
			} else {
				// Block and wait for subagent response in cooked mode
				select {
				case <-mam.ActiveAgent.Context.Done():
					fmt.Fprintln(os.Stderr, "Agent context cancelled.")
				case <-mam.ActiveAgent.Output:
				}
				fmt.Fprint(ppWriter, "\n\n")
			}
		} else {
			a.RunAgentLoop(os.Stderr, &messages, line, allowedTools, theme, false, currentSessionID)
		}
		activeTasks := 0
		for _, t := range a.ListTasks() {
			if t.Status == "running" {
				activeTasks++
			}
		}
		activeMessagesForTokens := messages
		if mam.ActiveAgent != nil {
			mam.ActiveAgent.HistoryMu.RLock()
			activeMessagesForTokens = mam.ActiveAgent.History
			mam.ActiveAgent.HistoryMu.RUnlock()
		}
		pTok, cTok := a.GetGlobalTokens(activeMessagesForTokens, allowedTools)
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
		DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
		stateMu.Lock()
		savedStats = lastStatsText
		stateMu.Unlock()
		DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
		DrawStatusBar(os.Stderr, theme)
	}

	ShutdownStatusBar(os.Stderr)
	fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: ./maquis --session %s (or ./maquis --resume)\n", currentSessionID)
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
	activeTheme := GetConfiguredTheme(a.Config)
	promptPrefix := "> "
	if kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]> ", kiReader.mam.ActiveAgent.Name)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	if rl != nil {
		rl.SetPrompt(promptPrefix)
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
	PrintBanner(cwBuf, a)
	activeMessages := []db.Message{}
	if kiReader.messages != nil {
		activeMessages = *kiReader.messages
	}
	if kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		kiReader.mam.ActiveAgent.HistoryMu.RLock()
		activeMessages = kiReader.mam.ActiveAgent.History
		kiReader.mam.ActiveAgent.HistoryMu.RUnlock()
	}
	if len(activeMessages) > 0 {
		PrintSessionHistory(cwBuf, activeMessages, activeTheme, a.Config)
	}

	// Calculate terminal size
	_, height := getTerminalSize()
	scrollBottom := height - 4 - pasteLinesOffset
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

	// 4b. Draw the last stats line
	stateMu.Lock()
	savedStats := lastStatsText
	stateMu.Unlock()
	DrawStaticStatsLine(cw, activeTheme, "", savedStats)

	// 5. Update status bar metrics and redraw status bar
	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	pTok, cTok := 0, 0
	activeMessagesForTokens := []db.Message{}
	if kiReader.messages != nil {
		activeMessagesForTokens = *kiReader.messages
	}
	if kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		kiReader.mam.ActiveAgent.HistoryMu.RLock()
		activeMessagesForTokens = kiReader.mam.ActiveAgent.History
		kiReader.mam.ActiveAgent.HistoryMu.RUnlock()
	}
	pTok, cTok = a.GetGlobalTokens(activeMessagesForTokens, nil)

	UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
	DrawStatusBar(cw, activeTheme)

	// 6. Draw input line and place cursor
	fmt.Fprintf(cw, "\x1b[%d;1H\x1b[2K", height-2-pasteLinesOffset)
	fmt.Fprint(cw, promptStr)
	fmt.Fprint(cw, kiReader.currentInputLine)
	fmt.Fprintf(cw, "\x1b[%d;%dH", height-2-pasteLinesOffset, 1+len(promptPrefix)+len(kiReader.currentInputLine))
}
