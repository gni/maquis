package ui
// [STDLIB-READ]: Mapping linux-amd64 file descriptors. Architecture: Byte-Buffered state loop, Max Matrix: 80x24 Strict, Mode: Raw syscall manipulation.


import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/unix"

	"maquis/pkg/agent"
	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
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
		"/config ",
		"/config show",
		"/config set ",
		"/clear",
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
		"/task ",
		"/task list",
		"/task view ",
		"/task stream ",
		"/task kill ",
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
	typeAheadBuffer  []byte
	isAtMainPrompt   bool
}

func (ki *keyInterceptorReader) Drain() {
	if ki.inputChan == nil {
		return
	}
	for {
		select {
		case <-ki.inputChan:
		default:
			return
		}
	}
}

func getPromptSymbol(cfg *config.Config) string {
	return "> "
}

func (ki *keyInterceptorReader) redrawTypeAhead() {
	activeTheme := GetConfiguredTheme(ki.agent.Config)
	promptPrefix := getPromptSymbol(ki.agent.Config)
	if ki.mam != nil && ki.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", ki.mam.ActiveAgent.Name, promptPrefix)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	_, height := getTerminalSize()
	if height <= 0 {
		return
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	getUI().StateMu.Lock()
	typeAheadCopy := string(ki.typeAheadBuffer)
	getUI().StateMu.Unlock()

	fmt.Fprintf(ki.w, "\x1b7\x1b[%d;1H\x1b[2K%s%s\x1b8", height-2-getUI().PasteLinesOffset, promptStr, typeAheadCopy)
}

func (ki *keyInterceptorReader) printCancelMessage() {
	fmt.Fprintln(ki.w, "\n\n[Operation Cancelled by User]")
}

func (ki *keyInterceptorReader) handleCtrlO() {

	runningTaskId := ki.agent.GetLastRunningTaskId()
	if runningTaskId != "" {
		ki.agent.ToggleStreaming(runningTaskId, ki.w)
	} else {
		ki.agent.Config.CollapseResults = !ki.agent.Config.CollapseResults
		_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
		SetCollapseStatus(ki.agent.Config.CollapseResults)
		
		redrawScreen(ki.w, ki.agent, ki, ki.rl)

		if ki.agent.CurrentWriter != nil {
			if fr, ok := ki.agent.CurrentWriter.(interface{ ForceReposition() }); ok {
				fr.ForceReposition()
			}
		}
	}
}

func (ki *keyInterceptorReader) handleCtrlT() {
	activeTheme := GetConfiguredTheme(ki.agent.Config)
	ki.agent.Config.ShowThinking = !ki.agent.Config.ShowThinking
	_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
	frame := agent.GetCurrentSpinnerFrame()
	DrawStaticPromptSeparatorWithSpinner(ki.w, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme, frame)
}

func (ki *keyInterceptorReader) handleCtrlR() {
	activeTheme := GetConfiguredTheme(ki.agent.Config)
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
	frame := agent.GetCurrentSpinnerFrame()
	DrawStaticPromptSeparatorWithSpinner(ki.w, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme, frame)
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
	promptPrefix := getPromptSymbol(ki.agent.Config)
	if ki.mam != nil && ki.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", ki.mam.ActiveAgent.Name, promptPrefix)
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

	getUI().LastH = 0

	var buf bytes.Buffer
	cwBuf := crnlWriter{w: &buf}

	if len(ki.agent.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, ki.agent.McpStartErrors, activeTheme)
	}

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

	_, height := getTerminalSize()
	scrollBottom := height - 5 - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	content := buf.String()
	linesCount := strings.Count(content, "\n")

	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{w: &finalBuf}

	// Write clear-screen first
	fmt.Fprint(cwFinal, "\x1b[r\x1b[H\x1b[J")

	// Write padding BEFORE content to push it to the bottom
	if linesCount < scrollBottom {
		padding := scrollBottom - linesCount
		fmt.Fprint(cwFinal, strings.Repeat("\n", padding))
	}

	fmt.Fprint(cwFinal, content)

	// Ensure the cursor scrolls/moves down past the scrolling region bottom
	// to align the last line of content at scrollBottom and prevent overlap with the status area.
	extraNewlines := height - scrollBottom
	if extraNewlines > 0 {
		fmt.Fprint(cwFinal, strings.Repeat("\n", extraNewlines))
	}

	DrawStaticPromptSeparator(cwFinal, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme)

	getUI().StateMu.Lock()
	savedStats := getUI().LastStatsText
	getUI().StateMu.Unlock()
	DrawStaticStatsLine(cwFinal, activeTheme, "", savedStats)

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
	DrawStatusBar(cwFinal, activeTheme)

	fmt.Fprintf(cwFinal, "\x1b[%d;1H\x1b[2K", height-2-getUI().PasteLinesOffset)

	_, _ = cw.Write(finalBuf.Bytes())
}

func (ki *keyInterceptorReader) GetInputChan() chan byte {
	return ki.inputChan
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

		ki.pastedText += string(normalized)

		if ki.w != nil {
			promptPrefix := "\r\x1b[32m" + getPromptSymbol(ki.agent.Config) + "\x1b[0m"
			if !ki.isAtMainPrompt {
				promptPrefix = "\r"
			}
			typeAheadCopy := string(ki.typeAheadBuffer)

			width, height := getTerminalSize()
			if width <= 0 {
				width = 80
			}
			maxPasteLines := height - 6
			if maxPasteLines < 1 {
				maxPasteLines = 1
			}

			lines := strings.Split(ki.pastedText, "\n")
			newlinesCount := len(lines) - 1

			if newlinesCount > 50 {
				getUI().PasteCounter++
				prefix := fmt.Sprintf("\x1b[90m[Pasted text #%d (+%d lines)]\x1b[0m", getUI().PasteCounter, newlinesCount)
				lines = []string{prefix}
				getUI().PasteLinesOffset = 0
			} else {
				physicalRowsNeeded := 0
				firstLineIdx := 0

				for i := len(lines) - 1; i >= 0; i-- {
					l := len(stripAnsi(lines[i]))
					if i == 0 {
						l += len(stripAnsi(promptPrefix)) + len(stripAnsi(typeAheadCopy))
					}
					rows := (l + width - 1) / width
					if l == 0 {
						rows = 1
					}

					if physicalRowsNeeded+rows > maxPasteLines && i != len(lines)-1 {
						firstLineIdx = i + 1
						break
					}
					physicalRowsNeeded += rows
				}

				if firstLineIdx > 0 {
					lines = lines[firstLineIdx:]
					hidden := firstLineIdx
					prefix := fmt.Sprintf("\x1b[90m... (%d lines hidden) ...\x1b[0m ", hidden)
					lines[0] = prefix + lines[0]

					// re-evaluate physical rows
					physicalRowsNeeded = 0
					for i := 0; i < len(lines); i++ {
						l := len(stripAnsi(lines[i]))
						if i == 0 {
							l += len(stripAnsi(promptPrefix)) + len(stripAnsi(typeAheadCopy))
						}
						rows := (l + width - 1) / width
						if l == 0 {
							rows = 1
						}
						physicalRowsNeeded += rows
					}
				}

				getUI().PasteLinesOffset = physicalRowsNeeded - 1
				if getUI().PasteLinesOffset < 0 {
					getUI().PasteLinesOffset = 0
				}
			}

			visualPastedText := strings.Join(lines, "\n")
			ki.redrawLayout()

			cw := crnlWriter{w: ki.w}
			_, _ = cw.Write([]byte(promptPrefix + typeAheadCopy + visualPastedText))
		}

		return 0, nil
	}

	writeIdx := 0
	for i := 0; i < n; i++ {
		b := p[i]
		if b == 3 { // Ctrl+C
			if ki.isAtMainPrompt {
				ki.ctrlCInterrupted = true
				p[writeIdx] = '\n'
				writeIdx++
			} else {
				p[writeIdx] = b
				writeIdx++
			}
		} else if b == 4 { // Ctrl+D
			p[writeIdx] = b
			writeIdx++
		} else if b == 20 || b == 18 || b == 15 { // Ctrl+T, Ctrl+R, or Ctrl+O
			activeTheme := GetConfiguredTheme(ki.agent.Config)
			if b == 20 { // Ctrl+T
				ki.agent.Config.ShowThinking = !ki.agent.Config.ShowThinking
				_ = config.SaveConfig(ki.agent.ConfigPath, ki.agent.Config)
				DrawStaticPromptSeparator(ki.w, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme)
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
				DrawStaticPromptSeparator(ki.w, ki.agent.Config.ShowThinking, ki.agent.Config.ReasoningEffort, activeTheme)
			} else if b == 15 { // Ctrl+O
				ki.handleCtrlO()
			}
		} else {
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}

func RunREPL(a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {
	IsInteractive = true
	defer func() { IsInteractive = false }()

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
	defer fmt.Fprint(os.Stderr, "\x1b[?25h")

	_, height := getTerminalSize()
	ppWriter := NewPromptPreservingWriter(os.Stderr, height)
	if uiImpl, ok := a.UI.(*AgentUIImpl); ok {
		uiImpl.ppWriter = ppWriter
	}

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
	a.MultiAgentManager = mam

	a.ClearAgentsFunc = func() {
		mam.ClearAllAgents()
	}

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
	getUI().ActiveInputReader = kiReader
	defer func() { getUI().ActiveInputReader = nil }()

	rawChan := make(chan byte, 1000)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := kiReader.r.Read(buf)
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
					close(rawChan)
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for i := 0; i < n; i++ {
				rawChan <- buf[i]
			}
		}
	}()

	go func() {
		for event := range a.SystemEvents {
			a.TasksMu.Lock()
			a.PendingSystemEvent = event
			a.TasksMu.Unlock()
			// \025 is Ctrl+U (clears the current input line), \n submits it
			for _, b := range []byte("\025\n") {
				kiReader.injectChan <- b
			}
		}
	}()

	go func() {
		for {
			b, ok := <-rawChan
			if !ok {
				close(kiReader.inputChan)
				return
			}

			getUI().StateMu.Lock()
			cancelFunc := getUI().ActiveCancelFunc
			getUI().StateMu.Unlock()

			if cancelFunc != nil {
				getUI().StateMu.Lock()
				inApproval := getUI().InApprovalPrompt
				getUI().StateMu.Unlock()

				if inApproval {
					if b == 3 || b == 4 { // Ctrl+C or Ctrl+D
						cancelFunc()
						kiReader.printCancelMessage()
						getUI().StateMu.Lock()
						kiReader.typeAheadBuffer = nil
						getUI().StateMu.Unlock()
						kiReader.inputChan <- b
						continue
					}
					if b == 27 { // Escape
						select {
						case next := <-rawChan:
							kiReader.inputChan <- b
							kiReader.inputChan <- next
						case <-time.After(50 * time.Millisecond):
							cancelFunc()
							kiReader.printCancelMessage()
							getUI().StateMu.Lock()
							kiReader.typeAheadBuffer = nil
							getUI().StateMu.Unlock()
							kiReader.inputChan <- b
						}
						continue
					}
					if b == 15 { // Ctrl+O
						kiReader.handleCtrlO()
						continue
					}
					if b == 20 { // Ctrl+T
						kiReader.handleCtrlT()
						continue
					}
					if b == 18 { // Ctrl+R
						kiReader.handleCtrlR()
						continue
					}
					kiReader.inputChan <- b
					continue
				}

				if b == 3 || b == 4 { // Ctrl+C or Ctrl+D
					cancelFunc()
					kiReader.printCancelMessage()
					getUI().StateMu.Lock()
					kiReader.typeAheadBuffer = nil
					getUI().StateMu.Unlock()
					continue
				}
				if b == 27 { // Escape
					select {
					case next := <-rawChan:
						kiReader.inputChan <- b
						kiReader.inputChan <- next
					case <-time.After(50 * time.Millisecond):
						cancelFunc()
						kiReader.printCancelMessage()
						getUI().StateMu.Lock()
						kiReader.typeAheadBuffer = nil
						getUI().StateMu.Unlock()
					}
					continue
				}
				if b == 15 { // Ctrl+O
					kiReader.handleCtrlO()
					continue
				}
				if b == 20 { // Ctrl+T
					kiReader.handleCtrlT()
					continue
				}
				if b == 18 { // Ctrl+R
					kiReader.handleCtrlR()
					continue
				}

				if b == 127 || b == 8 {
					getUI().StateMu.Lock()
					if len(kiReader.typeAheadBuffer) > 0 {
						r, size := utf8.DecodeLastRune(kiReader.typeAheadBuffer)
						if r != utf8.RuneError || size > 0 {
							kiReader.typeAheadBuffer = kiReader.typeAheadBuffer[:len(kiReader.typeAheadBuffer)-size]
						} else {
							kiReader.typeAheadBuffer = kiReader.typeAheadBuffer[:len(kiReader.typeAheadBuffer)-1]
						}
					}
					getUI().StateMu.Unlock()
					if len(rawChan) == 0 {
						kiReader.redrawTypeAhead()
					}
					kiReader.inputChan <- b
					continue
				}

				if b == 10 || b == 13 {
					getUI().StateMu.Lock()
					kiReader.typeAheadBuffer = nil
					getUI().StateMu.Unlock()
					kiReader.inputChan <- b
					continue
				}

				if b >= 32 || b == '\t' {
					getUI().StateMu.Lock()
					kiReader.typeAheadBuffer = append(kiReader.typeAheadBuffer, b)
					getUI().StateMu.Unlock()
					if len(rawChan) == 0 {
						kiReader.redrawTypeAhead()
					}
				}

				kiReader.inputChan <- b
			} else {
				kiReader.inputChan <- b
			}
		}
	}()

	rl := term.NewTerminal(kiReader, "")
	kiReader.rl = rl
	rl.History = hist

	if w, h, err := term.GetSize(fd); err == nil {
		rl.SetSize(w, h)
	}

	PrintBanner(ppWriter, a)

	SetCollapseStatus(a.Config.CollapseResults)
	UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
	DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
	getUI().StateMu.Lock()
	savedStats := getUI().LastStatsText
	getUI().StateMu.Unlock()
	DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
	DrawStatusBar(os.Stderr, theme)

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
					getUI().StateMu.Lock()
					idle := getUI().ActiveCancelFunc == nil
					getUI().StateMu.Unlock()
					if idle {
						redrawScreen(os.Stderr, a, kiReader, rl)
					} else {
						handleResize(os.Stderr, a, kiReader, rl)
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
		promptPrefix := getPromptSymbol(a.Config)
		if mam.ActiveAgent != nil {
			promptPrefix = fmt.Sprintf("[%s]%s", mam.ActiveAgent.Name, promptPrefix)
		}
		ppWriter.SetPromptCol(1 + utf8.RuneCountInString(promptPrefix))
		rl.SetPrompt(promptPrefix)
		TerminalMu.Lock()
		drawConsoleStaticControlsLocked(os.Stderr, a, kiReader, rl, false)
		TerminalMu.Unlock()

		fmt.Fprint(os.Stderr, "\x1b[?25h")
		ppWriter.cursorHidden = false

		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Printf("Error setting terminal raw mode: %v\n", err)
			os.Exit(1)
		}

		kiReader.isAtMainPrompt = true
		line, err := rl.ReadLine()
		kiReader.isAtMainPrompt = false
		term.Restore(fd, oldState)

		a.TasksMu.Lock()
		pendingEvent := a.PendingSystemEvent
		a.PendingSystemEvent = ""
		a.TasksMu.Unlock()
		if pendingEvent != "" {
			line = "/system_event " + pendingEvent
		}

		if height > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K", height-2-getUI().PasteLinesOffset)
			
			// Redraw prompt prefix so it doesn't disappear during stream
			promptPrefix := getPromptSymbol(a.Config)
			if kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
				promptPrefix = fmt.Sprintf("[%s]%s", kiReader.mam.ActiveAgent.Name, promptPrefix)
			}
			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprint(os.Stderr, promptStyle.Render(promptPrefix))
			ppWriter.SetPromptCol(1 + utf8.RuneCountInString(promptPrefix))
		}

		if kiReader.pastedText != "" {
			line = line + kiReader.pastedText
			kiReader.pastedText = ""
			getUI().PasteLinesOffset = 0
			InitStatusBar(os.Stderr)
		}

		if kiReader.ctrlCInterrupted {
			kiReader.ctrlCInterrupted = false
			kiReader.pastedText = ""
			getUI().PasteLinesOffset = 0
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
			redrawScreen(os.Stderr, a, kiReader, rl)
			continue
		}

		if err != nil {
			break
		}

		kiReader.currentInputLine = ""
		line = strings.ReplaceAll(line, "↵", "\n")

		if strings.TrimSpace(line) == "" {
			continue
		}

		if strings.HasPrefix(line, "/system_event ") {
			eventMsg := strings.TrimPrefix(line, "/system_event ")
			
			// Draw a small, subtle indicator instead of the giant prompt block
			eventStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			fmt.Fprintf(ppWriter, "\n%s\n", eventStyle.Render("✦ "+eventMsg))

			ctx, cancel := context.WithCancel(context.Background())
			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = cancel
			kiReader.typeAheadBuffer = nil
			getUI().StateMu.Unlock()

			restore, err := setNonCanonical(fd)

			ppWriter.SetCursorHidden(true)
			a.RunAgentLoop(ctx, ppWriter, &messages, eventMsg, allowedTools, theme, false, currentSessionID)
			ppWriter.SetCursorHidden(false)

			if err == nil && restore != nil {
				restore()
			}
			cancel()
			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = nil
			getUI().StateMu.Unlock()
			kiReader.Drain()

			redrawScreen(os.Stderr, a, kiReader, rl)
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		isCmd, cmdStr := parseManualCommand(line, a.Config.DirectCommands)
		if isCmd {
			isPureCd := false
			var target string
			if cmdStr == "cd" {
				isPureCd = true
			} else if strings.HasPrefix(cmdStr, "cd ") {
				rest := strings.TrimSpace(cmdStr[3:])
				if !strings.Contains(rest, "&&") && !strings.Contains(rest, ";") && !strings.Contains(rest, "|") {
					isPureCd = true
					target = rest
				}
			}

			if isPureCd {
				if target == "" {
					home, err := os.UserHomeDir()
					if err == nil {
						target = home
					}
				} else {
					// Strip surrounding quotes if present
					if (strings.HasPrefix(target, "\"") && strings.HasSuffix(target, "\"")) ||
						(strings.HasPrefix(target, "'") && strings.HasSuffix(target, "'")) {
						if len(target) >= 2 {
							target = target[1 : len(target)-1]
						}
					}
					// Expand ~ tilde
					if target == "~" {
						home, err := os.UserHomeDir()
						if err == nil {
							target = home
						}
					} else if strings.HasPrefix(target, "~/") {
						home, err := os.UserHomeDir()
						if err == nil {
							target = filepath.Join(home, target[2:])
						}
					} else if strings.HasPrefix(target, "~\\") {
						home, err := os.UserHomeDir()
						if err == nil {
							target = filepath.Join(home, target[2:])
						}
					}
				}

				err := os.Chdir(target)
				if err != nil {
					fmt.Fprintf(os.Stderr, "cd: %v\n", err)
				} else {
					pwd, _ := os.Getwd()
					a.WorkspaceRoot = pwd
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
			kiReader.Drain()
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

			isMutatingOrInteractive := isMutatingOrInteractiveSlashCommand(line)
			if !isMutatingOrInteractive {
				var buf bytes.Buffer
				cwBuf := crnlWriter{w: &buf}
				handled, quit = HandleSlashCommand(a, line, &messages, allowedTools, &theme, cwBuf, &currentSessionID, rl.History, mam, kiReader)
				kiReader.Drain()

				if handled && !quit {
					outputStr := tool.SanitizeUTF8(buf.Bytes())
					contextMsg := fmt.Sprintf("[user manually executed slash command: `%s`]\n%s", strings.TrimSpace(line), outputStr)
					messages = append(messages, db.Message{Role: "user", Content: contextMsg})
					_ = db.SaveMessage(currentSessionID, messages[len(messages)-1])

					redrawScreen(os.Stderr, a, kiReader, rl)
					continue
				}
			} else {
				ShutdownStatusBar(os.Stderr)
				cw := crnlWriter{w: os.Stderr}
				handled, quit = HandleSlashCommand(a, line, &messages, allowedTools, &theme, cw, &currentSessionID, rl.History, mam, kiReader)
				InitStatusBar(os.Stderr)
				kiReader.Drain()

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
						cmdName == "/clear" || cmdName == "/rewind" ||
						(cmdName == "/config" && len(parts) == 1) ||
						(cmdName == "/session" && (len(parts) == 1 || (len(parts) > 1 && parts[1] == "explorer"))) ||
						(cmdName == "/agent" && len(parts) == 1)

					if needsRedraw {
						redrawScreen(os.Stderr, a, kiReader, rl)
						continue
					}
				}
			}
		}

		if handled {
			if quit {
				break
			}
			DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			getUI().StateMu.Lock()
			savedStats = getUI().LastStatsText
			getUI().StateMu.Unlock()
			DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
			DrawStatusBar(os.Stderr, theme)
			continue
		}

		ppWriter.ForceReposition()

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

		if mam.ActiveAgent != nil {
			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = mam.ActiveAgent.CancelActiveTurn
			getUI().StateMu.Unlock()

			ppWriter.SetCursorHidden(true)
			err := mam.SendMessage(mam.ActiveAgent.Name, line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
			} else {
				select {
				case <-mam.ActiveAgent.Context.Done():
					fmt.Fprintln(os.Stderr, "Agent context cancelled.")
				case <-mam.ActiveAgent.Output:
				}
				fmt.Fprint(ppWriter, "\n\n")
			}
			ppWriter.SetCursorHidden(false)

			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = nil
			getUI().StateMu.Unlock()
			kiReader.Drain()
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = cancel
			kiReader.typeAheadBuffer = nil
			getUI().StateMu.Unlock()

			restore, err := setNonCanonical(fd)

			ppWriter.SetCursorHidden(true)
			a.RunAgentLoop(ctx, ppWriter, &messages, line, allowedTools, theme, false, currentSessionID)
			ppWriter.SetCursorHidden(false)

			if err == nil && restore != nil {
				restore()
			}
			cancel()
			getUI().StateMu.Lock()
			getUI().ActiveCancelFunc = nil
			getUI().StateMu.Unlock()
			kiReader.Drain()
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
		redrawScreen(os.Stderr, a, kiReader, rl)
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

func isMutatingOrInteractiveSlashCommand(line string) bool {
	trimmed := strings.TrimSpace(line)
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return false
	}
	cmdName := parts[0]
	if cmdName == "/agent" && len(parts) == 1 {
		return true
	}
	if cmdName == "/config" && len(parts) == 1 {
		return true
	}
	if cmdName == "/session" && len(parts) > 1 {
		op := parts[1]
		if op == "load" || op == "clear" || op == "new" || op == "branch" {
			return true
		}
	}
	if cmdName == "/clear" || cmdName == "/rewind" {
		return true
	}
	return false
}

func countWrappedLines(s string, termW int) int {
	if termW <= 0 {
		return strings.Count(s, "\n")
	}
	lines := strings.Split(s, "\n")
	count := 0
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			break
		}
		stripped := stripAnsi(line)
		length := utf8.RuneCountInString(stripped)
		if length == 0 {
			count += 1
		} else {
			count += (length + termW - 1) / termW
		}
	}
	if s == "" {
		return 0
	}
	return count
}

func drawConsoleStaticControlsLocked(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, drawPrompt bool) {
	_, height := getTerminalSize()
	if height <= 3 {
		return
	}
	activeTheme := GetConfiguredTheme(a.Config)
	
	// Draw Status Bar
	DrawStatusBarLocked(w, activeTheme)
	
	// Draw Separator
	DrawStaticPromptSeparatorLocked(w, a.Config.ShowThinking, a.Config.ReasoningEffort, activeTheme)

	// Draw Stats Line
	getUI().StateMu.Lock()
	savedStats := getUI().LastStatsText
	getUI().StateMu.Unlock()
	DrawStaticStatsLineLocked(w, activeTheme, "", savedStats)

	// Build and draw Prompt/Input
	promptPrefix := getPromptSymbol(a.Config)
	if kiReader != nil && kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", kiReader.mam.ActiveAgent.Name, promptPrefix)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	getUI().StateMu.Lock()
	inApproval := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()

	if inApproval {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", height-2-getUI().PasteLinesOffset)
		fmt.Fprint(w, promptStyle.Render(" Approve tool execution? [y/N/a (always)]: "))
	} else {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", height-2-getUI().PasteLinesOffset)
		if drawPrompt {
			fmt.Fprint(w, promptStr)
			inputLine := ""
			posOffset := 0
			if kiReader != nil {
				inputLine = kiReader.currentInputLine
				posOffset = len(inputLine)
			}
			if rl != nil {
				line, pos := getTerminalLine(rl)
				inputLine = line
				posOffset = pos
			}
			fmt.Fprint(w, inputLine)
			fmt.Fprintf(w, "\x1b[%d;%dH", height-2-getUI().PasteLinesOffset, 1+utf8.RuneCountInString(promptPrefix)+posOffset)
		}
	}
}

func redrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	SetCollapseStatus(a.Config.CollapseResults)
	activeTheme := GetConfiguredTheme(a.Config)
	promptPrefix := getPromptSymbol(a.Config)
	if kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", kiReader.mam.ActiveAgent.Name, promptPrefix)
	}

	if rl != nil {
		rl.SetPrompt(promptPrefix)
	}

	getUI().LastH = 0

	var buf bytes.Buffer
	cwBuf := crnlWriter{w: &buf}

	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, a.McpStartErrors, activeTheme)
	}

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

	termW, height := getTerminalSize()
	scrollBottom := height - 5 - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	content := buf.String()
	linesCount := countWrappedLines(content, termW)

	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{w: &finalBuf}

	// Write clear-screen first
	fmt.Fprint(cwFinal, "\x1b[r\x1b[H\x1b[J")

	// Write padding BEFORE content to push it to the bottom
	if linesCount < scrollBottom {
		padding := scrollBottom - linesCount
		fmt.Fprint(cwFinal, strings.Repeat("\n", padding))
	}

	fmt.Fprint(cwFinal, content)

	// Ensure the cursor scrolls/moves down past the scrolling region bottom
	extraNewlines := height - scrollBottom
	if extraNewlines > 0 {
		fmt.Fprint(cwFinal, strings.Repeat("\n", extraNewlines))
	}

	a.CurrentStreamMu.Lock()
	if a.CurrentStreamBuffer != nil {
		b := a.CurrentStreamBuffer.Bytes()
		if len(b) > 0 {
			cwFinal.Write(b)
		}
	}
	a.CurrentStreamMu.Unlock()

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

	drawConsoleStaticControlsLocked(cwFinal, a, kiReader, rl, true)

	_, _ = w.Write(finalBuf.Bytes())
}

func setNonCanonical(fd int) (func(), error) {
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	oldTermios := *termios

	termios.Lflag &^= unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	err = unix.IoctlSetTermios(fd, unix.TCSETS, termios)
	if err != nil {
		return nil, err
	}

	restore := func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &oldTermios)
	}
	return restore, nil
}

func getTerminalLine(rl *term.Terminal) (string, int) {
	if rl == nil {
		return "", 0
	}
	val := reflect.ValueOf(rl).Elem()
	lineField := val.FieldByName("line")
	posField := val.FieldByName("pos")
	if lineField.IsValid() && posField.IsValid() {
		ptrLine := unsafe.Pointer(lineField.UnsafeAddr())
		runes := *(*[]rune)(ptrLine)

		ptrPos := unsafe.Pointer(posField.UnsafeAddr())
		pos := *(*int)(ptrPos)

		return string(runes), pos
	}
	return "", 0
}

func handleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	width, height := getTerminalSize()
	if height <= 3 {
		return
	}

	if rl != nil {
		rl.SetSize(width, height)
	}

	cw := crnlWriter{w: w}
	drawConsoleStaticControlsLocked(cw, a, kiReader, rl, true)

	if a.CurrentWriter != nil {
		if fr, ok := a.CurrentWriter.(interface{ ForceReposition() }); ok {
			fr.ForceReposition()
		}
	}
}