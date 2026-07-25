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

	"golang.org/x/term"
	"maquis/pkg/agent"
	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
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
						matches = append(matches, subCmd+pk)
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
	fullMap map[string]string
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
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return
	}
	if h.fullMap == nil {
		h.fullMap = make(map[string]string)
	}

	display := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", " ↵ "), "\n", " ↵ ")
	display = strings.TrimSpace(display)

	h.fullMap[display] = raw
	h.fullMap[raw] = raw

	h.entries = append(h.entries, display)
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

func (h *customHistory) GetFull(line string) string {
	if h.fullMap == nil {
		return line
	}
	if full, ok := h.fullMap[line]; ok && full != "" {
		return full
	}
	return line
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
	allowedTools     []string
	inputChan        chan byte
	approvalChan     chan byte
	approvalReader   *approvalByteReader
	injectChan       chan byte
	typeAheadBuffer  []byte
	inBracketedPaste bool
	isAtMainPrompt   bool
	lastCtrlDTime    time.Time
	pastedCodeBlocks map[string]string
}

type approvalByteReader struct {
	input <-chan byte
}

func activeToolAllowlist(reader *keyInterceptorReader) []string {
	if reader == nil {
		return nil
	}
	return reader.allowedTools
}

func calculateActiveTokenUsage(
	a *agent.Agent,
	messages []db.Message,
	allowedTools []string,
	mam *agent.MultiAgentManager,
) (int, int, bool) {
	activeMessages := messages
	if mam != nil && mam.ActiveAgent != nil {
		mam.ActiveAgent.HistoryMu.RLock()
		activeMessages = append([]db.Message(nil), mam.ActiveAgent.History...)
		mam.ActiveAgent.HistoryMu.RUnlock()
	}
	return a.GetGlobalTokenUsage(activeMessages, allowedTools)
}

func (r *approvalByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	first, ok := <-r.input
	if !ok {
		return 0, io.EOF
	}
	p[0] = first
	if first != 27 || len(p) == 1 {
		return 1, nil
	}

	timer := time.NewTimer(15 * time.Millisecond)
	defer timer.Stop()

	n := 1
	for n < len(p) && n < 3 {
		select {
		case next, channelOpen := <-r.input:
			if !channelOpen {
				return n, nil
			}
			p[n] = next
			n++
		case <-timer.C:
			return n, nil
		}
	}
	return n, nil
}

func (ki *keyInterceptorReader) BeginApprovalInput() io.Reader {
	if ki.approvalChan == nil {
		ki.approvalChan = make(chan byte, 1000)
	}
	ki.drainApprovalInput()
	if ki.approvalReader == nil {
		ki.approvalReader = &approvalByteReader{input: ki.approvalChan}
	}
	return ki.approvalReader
}

func (ki *keyInterceptorReader) EndApprovalInput() {
	ki.drainApprovalInput()
}

func (ki *keyInterceptorReader) drainApprovalInput() {
	if ki.approvalChan == nil {
		return
	}
	for {
		select {
		case _, channelOpen := <-ki.approvalChan:
			if !channelOpen {
				return
			}
		default:
			return
		}
	}
}

var promptNavigationKeyReplacer = strings.NewReplacer(
	"\x1b[1;5D", "\x1b[1;3D",
	"\x1b[1;5C", "\x1b[1;3C",
	"\x1b[5D", "\x1b[1;3D",
	"\x1b[5C", "\x1b[1;3C",
	"\x1bO1;5D", "\x1b[1;3D",
	"\x1bO1;5C", "\x1b[1;3C",
)

// normalizePromptNavigationKeys translates the Ctrl+Arrow sequences emitted
// by common terminals into the word-navigation sequences understood by x/term.
// Ctrl+A is already handled natively by x/term and passes through unchanged.
func normalizePromptNavigationKeys(input []byte) []byte {
	if !bytes.ContainsRune(input, '\x1b') {
		return input
	}
	return []byte(promptNavigationKeyReplacer.Replace(string(input)))
}

func (ki *keyInterceptorReader) copyReadData(p, data []byte) int {
	n := copy(p, data)
	if n < len(data) {
		ki.pasteRemaining = append([]byte(nil), data[n:]...)
	}
	return n
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

	termW, height := getTerminalSize()
	if height <= 0 {
		return
	}

	getUI().StateMu.Lock()
	typeAheadCopy := string(ki.typeAheadBuffer)
	pasteLinesOffset := getUI().PasteLinesOffset
	getUI().StateMu.Unlock()

	prefixLen := utf8.RuneCountInString(stripAnsi(promptPrefix))
	availableWidth := termW - prefixLen - 1
	if availableWidth < 1 {
		availableWidth = 1
	}

	inputRunes := []rune(typeAheadCopy)
	if len(inputRunes) > availableWidth {
		inputRunes = inputRunes[len(inputRunes)-availableWidth:]
	}
	displayInput := string(inputRunes)
	cursorCol := prefixLen + len(inputRunes) + 1
	if termW > 0 && cursorCol > termW {
		cursorCol = termW
	}

	if ki.agent != nil && ki.agent.CurrentWriter != nil {
		if writer, ok := ki.agent.CurrentWriter.(*PromptPreservingWriter); ok {
			writer.SetPromptCol(cursorCol)
		}
	}

	promptRow := height - 2 - pasteLinesOffset
	if promptRow < 1 {
		promptRow = 1
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	fmt.Fprintf(ki.w, "\x1b[%d;1H\x1b[2K%s%s\x1b[%d;%dH", promptRow, promptStr, displayInput, promptRow, cursorCol)
}

func (ki *keyInterceptorReader) printCancelMessage() {
	output := ki.w
	if ki.agent != nil {
		if uiImpl, ok := ki.agent.UI.(*AgentUIImpl); ok && uiImpl.ppWriter != nil {
			output = uiImpl.ppWriter
		}
	}
	fmt.Fprintln(output, "\n[Operation Cancelled by User]")
}

func (ki *keyInterceptorReader) handleSubagentCancellation(rawInput <-chan byte, cancelParent context.CancelFunc, agentName string) {
	output := ki.w
	if ki.agent != nil && ki.agent.CurrentWriter != nil {
		output = ki.agent.CurrentWriter
	}
	decision := askForSubagentCancellation(
		output,
		&approvalByteReader{input: rawInput},
		GetConfiguredTheme(ki.agent.Config),
		agentName,
	)

	switch decision {
	case agent.SubagentCancellationContinue:
		fmt.Fprintln(output, "\n[Subagent cancellation dismissed]")
	case agent.SubagentCancellationSkipCurrent:
		if ki.mam.CancelSubagentTurn(agentName) {
			fmt.Fprintf(output, "\n[Skipped subagent: %s]\n", agentName)
		} else {
			fmt.Fprintf(output, "\n[Subagent already finished: %s]\n", agentName)
		}
	case agent.SubagentCancellationStopAll:
		ki.mam.CancelAllActiveSubagents()
		if cancelParent != nil {
			cancelParent()
		}
		fmt.Fprintln(output, "\n\n[Operation Cancelled by User]")
	}

	getUI().StateMu.Lock()
	ki.typeAheadBuffer = nil
	getUI().StateMu.Unlock()
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
		return len(p), nil
	}

	if ki.isAtMainPrompt && ki.rl != nil {
		termW, height := getTerminalSize()
		if height > 3 {
			if termW <= 0 {
				termW = 80
			}
			line, pos := getTerminalLine(ki.rl)
			prefixLen := utf8.RuneCountInString(stripAnsi(promptPrefix))
			availWidth := termW - prefixLen - 1
			if availWidth < 10 {
				availWidth = 10
			}

			runes := []rune(line)
			totalRunes := len(runes)

			displayStr := line
			cursorCol := prefixLen + pos + 1

			if totalRunes > availWidth {
				start := pos - (availWidth / 2)
				if start < 0 {
					start = 0
				}
				end := start + availWidth
				if end > totalRunes {
					end = totalRunes
					start = end - availWidth
					if start < 0 {
						start = 0
					}
				}
				displayStr = string(runes[start:end])
				cursorCol = prefixLen + (pos - start) + 1
			}

			var buf bytes.Buffer
			cursorVisibility := "\x1b[?25h"
			if ki.agent != nil && ki.agent.CurrentWriter != nil {
				if pw, ok := ki.agent.CurrentWriter.(*PromptPreservingWriter); ok && pw.cursorHidden {
					cursorVisibility = "\x1b[?25l"
				}
			}
			fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s%s\x1b[%d;%dH%s", height-2, promptStr, displayStr, height-2, cursorCol, cursorVisibility)
			_, _ = ki.writeToTerminal(buf.Bytes())
			return len(p), nil
		}
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

	// Set scroll region BEFORE rendering content so text auto-scrolls within 1..scrollBottom
	fmt.Fprintf(cwFinal, "\x1b[1;%dr\x1b[1;1H", scrollBottom)

	// Write padding BEFORE content to push it to the bottom of the scroll region
	if linesCount < scrollBottom {
		padding := scrollBottom - linesCount
		fmt.Fprint(cwFinal, strings.Repeat("\n", padding))
	}

	fmt.Fprint(cwFinal, content)

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

	activeMessagesForTokens := []db.Message{}
	if ki.messages != nil {
		activeMessagesForTokens = *ki.messages
	}
	pTok, cTok, estimated := calculateActiveTokenUsage(ki.agent, activeMessagesForTokens, activeToolAllowlist(ki), ki.mam)

	UpdateStatus(ki.agent.Config.Model, pTok, cTok, 0, ki.agent.Config.ContextWindowLimit, false, 0, activeTasks, ki.agent.Config.ShowTokens, estimated)
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

		if n == 1 && ki.inputChan != nil {
			select {
			case input, ok := <-ki.inputChan:
				if ok {
					p[n] = input
					n++
				}
			case <-time.After(3 * time.Millisecond):
			}
		}

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

	wasBracketedPaste := bytes.Contains(p[:n], []byte("\x1b[200~")) || ki.inBracketedPaste
	if wasBracketedPaste {
		ki.inBracketedPaste = true
		data := p[:n]
		if idx := bytes.Index(data, []byte("\x1b[200~")); idx >= 0 {
			data = data[idx+6:]
		}
		if idx := bytes.Index(data, []byte("\x1b[201~")); idx >= 0 {
			data = data[:idx]
			ki.inBracketedPaste = false
		}
		n = copy(p, data)
	}

	if !wasBracketedPaste {
		n = ki.copyReadData(p, normalizePromptNavigationKeys(p[:n]))
	}

	if n > 1 {
		var rawBytes []byte
		for i := 0; i < n; i++ {
			if p[i] == '\r' {
				if i+1 < n && p[i+1] == '\n' {
					i++
				}
				rawBytes = append(rawBytes, '\n')
			} else {
				rawBytes = append(rawBytes, p[i])
			}
		}
		rawStr := string(rawBytes)
		lines := strings.Split(rawStr, "\n")

		if len(lines) > 3 || len(rawStr) > 180 {
			if ki.pastedCodeBlocks == nil {
				ki.pastedCodeBlocks = make(map[string]string)
			}
			getUI().PasteCounter++
			tagStr := fmt.Sprintf("[Pasted code #%d (+%d lines, %d chars)]", getUI().PasteCounter, len(lines)-1, len(rawStr))
			ki.pastedCodeBlocks[tagStr] = rawStr
			n = copy(p, []byte(tagStr))
		} else {
			normalizedStr := strings.ReplaceAll(rawStr, "\n", " ↵ ")
			n = copy(p, []byte(normalizedStr))
		}
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

	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	clearTerminalForStartup(os.Stderr)
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
		r:            os.Stdin,
		agent:        a,
		theme:        theme,
		w:            os.Stderr,
		messages:     &messages,
		mam:          mam,
		allowedTools: allowedTools,
		inputChan:    make(chan byte, 1000),
		approvalChan: make(chan byte, 1000),
		injectChan:   make(chan byte, 100),
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
				close(kiReader.approvalChan)
				return
			}

			getUI().StateMu.Lock()
			cancelFunc := getUI().ActiveCancelFunc
			inApproval := getUI().InApprovalPrompt
			getUI().StateMu.Unlock()

			if inApproval {
				if b == 3 || b == 4 { // Ctrl+C or Ctrl+D
					if cancelFunc != nil {
						cancelFunc()
					}
					kiReader.printCancelMessage()
					getUI().StateMu.Lock()
					kiReader.typeAheadBuffer = nil
					getUI().StateMu.Unlock()
					kiReader.approvalChan <- b
					continue
				}
				if b == 27 { // Escape
					select {
					case next := <-rawChan:
						kiReader.approvalChan <- b
						kiReader.approvalChan <- next
					case <-time.After(50 * time.Millisecond):
						if cancelFunc != nil {
							cancelFunc()
						}
						kiReader.printCancelMessage()
						getUI().StateMu.Lock()
						kiReader.typeAheadBuffer = nil
						getUI().StateMu.Unlock()
						kiReader.approvalChan <- b
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
				kiReader.approvalChan <- b
				continue
			}

			if b == 3 || b == 4 {
				if kiReader.mam != nil {
					if activeName, active := kiReader.mam.ActiveSubagentName(); active {
						kiReader.handleSubagentCancellation(rawChan, cancelFunc, activeName)
						continue
					}
				}
			}

			if cancelFunc != nil {
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
						if next == '[' || next == 'O' {
							select {
							case dir := <-rawChan:
								kiReader.inputChan <- dir
								time.Sleep(2 * time.Millisecond)
								TerminalMu.Lock()
								drawConsoleStaticControlsLocked(os.Stderr, kiReader.agent, kiReader, kiReader.rl, true)
								TerminalMu.Unlock()
							case <-time.After(20 * time.Millisecond):
							}
						}
					case <-time.After(50 * time.Millisecond):
						if cancelFunc != nil {
							cancelFunc()
							kiReader.printCancelMessage()
							getUI().StateMu.Lock()
							kiReader.typeAheadBuffer = nil
							getUI().StateMu.Unlock()
						}
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
				if b == 4 { // Ctrl+D at main prompt
					line, _ := getTerminalLine(kiReader.rl)
					if strings.TrimSpace(line) == "" {
						now := time.Now()
						if now.Sub(kiReader.lastCtrlDTime) < 3*time.Second {
							kiReader.inputChan <- 4
							return
						} else {
							kiReader.lastCtrlDTime = now
							activeTheme := GetConfiguredTheme(a.Config)
							hintStyle := style.NewStyle().Foreground(activeTheme.Border).Italic(true)
							fmt.Fprintf(os.Stderr, "  %s", hintStyle.Render("(Press Ctrl+D again to exit)"))
							continue
						}
					}
				}
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

	initialPromptTokens, initialCompletionTokens, initialTokensEstimated := calculateActiveTokenUsage(a, messages, allowedTools, mam)

	PrintBanner(ppWriter, a)

	SetCollapseStatus(a.Config.CollapseResults)
	UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens, initialTokensEstimated)
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
		rl.SetPrompt("")
		TerminalMu.Lock()
		drawConsoleStaticControlsLocked(os.Stderr, a, kiReader, rl, true)
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
			kiReader.currentInputLine = ""
			kiReader.pastedText = ""
			kiReader.pastedCodeBlocks = nil
			getUI().StateMu.Lock()
			kiReader.typeAheadBuffer = nil
			getUI().StateMu.Unlock()
			activeTasks := 0
			for _, t := range a.ListTasks() {
				if t.Status == "running" {
					activeTasks++
				}
			}
			pTok, cTok, estimated := calculateActiveTokenUsage(a, messages, allowedTools, mam)
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)
			refreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader, rl)
			continue
		}

		if err != nil {
			break
		}

		kiReader.currentInputLine = ""
		line = strings.ReplaceAll(line, "↵", "\n")
		if kiReader.pastedCodeBlocks != nil {
			for tag, code := range kiReader.pastedCodeBlocks {
				if strings.Contains(line, tag) {
					line = strings.ReplaceAll(line, tag, code)
				}
			}
			kiReader.pastedCodeBlocks = nil
		}

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

			a.RunAgentLoop(ctx, ppWriter, &messages, eventMsg, allowedTools, theme, false, currentSessionID)

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
			handled, quit = HandleSlashCommand(a, line, &messages, allowedTools, &theme, os.Stderr, &currentSessionID, rl.History, mam, kiReader)
			kiReader.Drain()

			if handled {
				if quit {
					break
				}
				continue
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

			a.RunAgentLoop(ctx, ppWriter, &messages, line, allowedTools, theme, false, currentSessionID)

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
		pTok, cTok, estimated := calculateActiveTokenUsage(a, messages, allowedTools, mam)
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)
		refreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
	}

	ShutdownStatusBar(os.Stderr)
	fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: ./maquis --session %s (or ./maquis --resume)\n", currentSessionID)
}

func clearTerminalForStartup(w io.Writer) {
	// Reset any scrolling region left by an earlier process before erasing the
	// complete visible viewport. This prevents shell and prior-session text
	// from surviving above the TUI's bottom-aligned banner.
	fmt.Fprint(w, "\x1b[r\x1b[2J\x1b[H")
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
		"ls": true,
		"cd": true,
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
	termW, height := getTerminalSize()
	if height <= 3 {
		return
	}
	if termW <= 0 {
		termW = 80
	}
	activeTheme := GetConfiguredTheme(a.Config)

	promptPrefix := getPromptSymbol(a.Config)
	if kiReader != nil && kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", kiReader.mam.ActiveAgent.Name, promptPrefix)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	getUI().StateMu.Lock()
	inApproval := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()

	inputLine := ""
	posOffset := 0
	if drawPrompt {
		if rl != nil {
			inputLine, posOffset = getTerminalLine(rl)
		} else if kiReader != nil {
			inputLine = kiReader.currentInputLine
			posOffset = len(inputLine)
		}
		if kiReader != nil && kiReader.pastedText != "" {
			inputLine = inputLine + kiReader.pastedText
			posOffset = len([]rune(inputLine))
		}
	}

	inputLine = strings.ReplaceAll(inputLine, "\t", " ")
	inputLine = strings.ReplaceAll(inputLine, "\r", "")
	inputLine, posOffset = normalizeHistoryInput(inputLine, posOffset)

	layout := CalculatePromptLayout(promptPrefix, inputLine, posOffset, termW)

	getUI().StateMu.Lock()
	oldOffset := getUI().PasteLinesOffset
	getUI().PasteLinesOffset = layout.ExtraOffset
	getUI().StateMu.Unlock()

	maxOffset := oldOffset
	if layout.ExtraOffset > maxOffset {
		maxOffset = layout.ExtraOffset
	}

	promptStartRow := height - 2 - layout.ExtraOffset
	if promptStartRow < 1 {
		promptStartRow = 1
	}

	var clearBuf bytes.Buffer
	for l := height - 4 - maxOffset; l <= height-2; l++ {
		if l >= 1 {
			fmt.Fprintf(&clearBuf, "\x1b[%d;1H\x1b[2K", l)
		}
	}
	_, _ = w.Write(clearBuf.Bytes())

	// Draw Status Bar at row height
	DrawStatusBarLocked(w, activeTheme)

	// Draw Separator at row height-3-PasteLinesOffset
	DrawStaticPromptSeparatorLocked(w, a.Config.ShowThinking, a.Config.ReasoningEffort, activeTheme)

	// Draw Stats Line at row height-4-PasteLinesOffset
	getUI().StateMu.Lock()
	savedStats := getUI().LastStatsText
	getUI().StateMu.Unlock()
	DrawStaticStatsLineLocked(w, activeTheme, "", savedStats)

	if inApproval {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", promptStartRow)
		fmt.Fprint(w, promptStyle.Render(" Approve tool execution? [y/N/a (always)]: "))
	} else {
		lines := strings.Split(inputLine, "\n")
		var pBuf bytes.Buffer

		if len(lines) > 1 {
			prefixLen := utf8.RuneCountInString(stripAnsi(promptPrefix))
			indent := strings.Repeat(" ", prefixLen)
			for i, line := range lines {
				row := promptStartRow + i
				if row > height-2 {
					break
				}
				fmt.Fprintf(&pBuf, "\x1b[%d;1H\x1b[2K", row)
				if i == 0 {
					pBuf.WriteString(promptStr)
				} else {
					pBuf.WriteString(indent)
				}
				pBuf.WriteString(line)
			}
		} else {
			prefixLen := utf8.RuneCountInString(stripAnsi(promptPrefix))
			availWidth := termW - prefixLen - 1
			if availWidth < 10 {
				availWidth = 10
			}

			runes := []rune(inputLine)
			totalRunes := len(runes)

			displayStr := inputLine
			if totalRunes > availWidth {
				start := posOffset - (availWidth / 2)
				if start < 0 {
					start = 0
				}
				end := start + availWidth
				if end > totalRunes {
					end = totalRunes
					start = end - availWidth
					if start < 0 {
						start = 0
					}
				}
				displayStr = string(runes[start:end])
			}

			fmt.Fprintf(&pBuf, "\x1b[%d;1H\x1b[2K", promptStartRow)
			pBuf.WriteString(promptStr)
			if displayStr != "" {
				pBuf.WriteString(displayStr)
			}
		}

		cursorRow := promptStartRow + layout.CursorRow
		if cursorRow > height-2 {
			cursorRow = height - 2
		}

		if drawPrompt {
			fmt.Fprintf(&pBuf, "\x1b[%d;%dH\x1b[?25h", cursorRow, layout.CursorCol)
		} else {
			prefixLen := utf8.RuneCountInString(stripAnsi(promptPrefix))
			promptCol := 1 + prefixLen
			fmt.Fprintf(&pBuf, "\x1b[%d;%dH", promptStartRow, promptCol)
		}

		_, _ = w.Write(pBuf.Bytes())
	}
}

func redrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	redrawScreenWithNotice(w, a, kiReader, rl, "")
}

func refreshConsoleAfterTurn(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	drawConsoleStaticControlsLocked(w, a, kiReader, rl, true)
}

// refreshConsoleAfterPromptCancellation redraws only the fixed controls at the
// bottom of the terminal. A full history repaint would erase live-only chat
// presentation and discard terminal scrollback.
func refreshConsoleAfterPromptCancellation(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	refreshConsoleAfterTurn(w, a, kiReader, rl)
}

func redrawScreenWithNotice(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, notice string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	SetCollapseStatus(a.Config.CollapseResults)
	activeTheme := GetConfiguredTheme(a.Config)

	if rl != nil {
		rl.SetPrompt("")
	}

	getUI().LastH = 0

	var buf bytes.Buffer
	cwBuf := crnlWriter{w: &buf}

	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, a.McpStartErrors, activeTheme)
	}

	PrintBanner(cwBuf, a)
	if notice != "" {
		noticeStyle := style.NewStyle().Foreground(activeTheme.Success).Italic(true)
		fmt.Fprintln(cwBuf, noticeStyle.Render(notice))
		fmt.Fprintln(cwBuf)
	}

	activeMessages := []db.Message{}
	if kiReader != nil && kiReader.messages != nil {
		activeMessages = *kiReader.messages
	}
	if kiReader != nil && kiReader.mam != nil && kiReader.mam.ActiveAgent != nil {
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

	// Set scroll region BEFORE rendering content so text auto-scrolls within 1..scrollBottom
	fmt.Fprintf(cwFinal, "\x1b[1;%dr\x1b[1;1H", scrollBottom)

	// Write padding BEFORE content to push it to the bottom of the scroll region
	if linesCount < scrollBottom {
		padding := scrollBottom - linesCount
		fmt.Fprint(cwFinal, strings.Repeat("\n", padding))
	}

	fmt.Fprint(cwFinal, content)

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

	activeMessagesForTokens := []db.Message{}
	if kiReader != nil && kiReader.messages != nil {
		activeMessagesForTokens = *kiReader.messages
	}
	var mam *agent.MultiAgentManager
	if kiReader != nil {
		mam = kiReader.mam
	}
	pTok, cTok, estimated := calculateActiveTokenUsage(a, activeMessagesForTokens, activeToolAllowlist(kiReader), mam)

	UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)

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
