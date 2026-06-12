package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"bidouille/pkg/ui/style"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
)


func (a *Agent) RunAgentLoop(w io.Writer, messages *[]db.Message, prompt string, allowlist []string, theme ui.UITheme, isNonInteractive bool, sessionID string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	var totalCompletionTokens int
	var totalPromptTokens int
	var totalApiDuration time.Duration

	startTime := time.Now()
	timePrinted := false
	defer func() {
		if !timePrinted && prompt != "" {
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)
			fmt.Fprintln(w)
			fmt.Fprintln(w, timeStyled)
		}
	}()

	*messages = append(*messages, db.Message{Role: "user", Content: prompt})
	if sessionID != "" {
		if !db.HasMessages(sessionID) {
			if len(*messages) > 1 && (*messages)[0].Role == "system" {
				_ = db.SaveMessage(sessionID, (*messages)[0])
			}
		}
		_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	go func() {
		select {
		case <-sigChan:
			fmt.Fprintln(w, "\n\n[Operation Cancelled by User]")
			cancel()
		case <-ctx.Done():
		}
	}()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 9999
	}
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
		}

		if iter > 1 {
			width, _, err := term.GetSize(int(os.Stderr.Fd()))
			if err != nil || width <= 0 {
				width = 80
			}
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", width))
			fmt.Fprintln(w)
			fmt.Fprintln(w, divider)
		}

		chunkChan := make(chan StreamChunk, 200)
		streamErrChan := make(chan error, 1)
		var assistantMsg *db.Message

		go func() {
			msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, chunkChan)
			streamErrChan <- err
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
		}()

		ncw := &newlineCounterWriter{Writer: w}
		sr := ui.NewStreamRenderer(ncw, theme, a.Config.ShowThinking)

		stopKeyListen := make(chan struct{})
		keyListenDone := make(chan struct{})
		go func() {
			defer close(keyListenDone)
			fd := int(os.Stdin.Fd())
			if !term.IsTerminal(fd) {
				return
			}
			termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
			if err != nil {
				return
			}
			oldTermios := *termios

			termios.Lflag &^= unix.ICANON | unix.ECHO
			termios.Cc[unix.VMIN] = 1
			termios.Cc[unix.VTIME] = 0

			err = unix.IoctlSetTermios(fd, unix.TCSETS, termios)
			if err != nil {
				return
			}
			defer func() {
				_ = unix.IoctlSetTermios(fd, unix.TCSETS, &oldTermios)
			}()

			_ = syscall.SetNonblock(fd, true)
			defer syscall.SetNonblock(fd, false)

			buf := make([]byte, 1)
			for {
				select {
				case <-stopKeyListen:
					return
				default:
					n, err := os.Stdin.Read(buf)
					if err == nil && n > 0 {
						if buf[0] == 15 { // Ctrl+O
							runningTaskId := a.GetLastRunningTaskId()
							if runningTaskId != "" {
								a.ToggleStreaming(runningTaskId, w)
							} else {
								a.Config.CollapseResults = !a.Config.CollapseResults
								_ = config.SaveConfig(a.ConfigPath, a.Config)
								status := "EXPANDED"
								if a.Config.CollapseResults {
									status = "COLLAPSED"
								}
								infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
								fmt.Fprintf(w, "\n%s\n", infoStyle.Render(fmt.Sprintf("[Ctrl+O: Tool results will be %s]", status)))
							}
						} else if buf[0] == 20 { // Ctrl+T
							a.Config.ShowThinking = !a.Config.ShowThinking
							_ = config.SaveConfig(a.ConfigPath, a.Config)
							status := "ENABLED"
							if !a.Config.ShowThinking {
								status = "DISABLED"
							}
							infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
							fmt.Fprintf(w, "\n%s\n", infoStyle.Render(fmt.Sprintf("[Ctrl+T: Thinking is now %s]", status)))
						} else if buf[0] == 18 { // Ctrl+R
							nextEffort := "low"
							switch strings.ToLower(a.Config.ReasoningEffort) {
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
							a.Config.ReasoningEffort = nextEffort
							_ = config.SaveConfig(a.ConfigPath, a.Config)
							infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
							fmt.Fprintf(w, "\n%s\n", infoStyle.Render(fmt.Sprintf("[Ctrl+R: Reasoning effort set to %s]", nextEffort)))
						} else if buf[0] == 3 || buf[0] == 27 { // Ctrl+C or Escape
							fmt.Fprintln(w, "\n\n[Operation Cancelled by User]")
							cancel()
							return
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}()

		parser := &jsonStreamParser{activeToolIndex: -1}

		globalPromptTokensEst, priorCompletionTokens := a.GetGlobalTokens(*messages, allowlist)

		var reasoningChars int
		var textChars int
		var toolCallChars int
		var generationStart time.Time

		for chunk := range chunkChan {
			if chunk.Type == "reasoning" {
				if generationStart.IsZero() {
					generationStart = time.Now()
				}
				sr.WriteReasoning(chunk.Content)
				reasoningChars += len(chunk.Content)
			} else if chunk.Type == "text" {
				if generationStart.IsZero() {
					generationStart = time.Now()
				}
				sr.Write(chunk.Content)
				textChars += len(chunk.Content)
			} else if chunk.Type == "tool_name" {
				if parser.activeToolIndex != chunk.ToolCallIndex || parser.activeToolName == "" {
					sr.Flush()
					parser.needsLeadingNewline = sr.HasOutput()
					parser.activeToolName = chunk.Content
					parser.activeToolIndex = chunk.ToolCallIndex
					parser.titlePrinted = false
					parser.path = ""
					parser.pathPrinted = false
				} else {
					parser.activeToolName = chunk.Content
				}
			} else if chunk.Type == "tool_call" {
				parser.feed(chunk.Content, ncw, theme)
				toolCallChars += len(chunk.Content)
			}

			if !isNonInteractive {
				currentCompletionTokensEst := (reasoningChars + textChars + toolCallChars) / 4
				globalCompletionTokensEst := priorCompletionTokens + currentCompletionTokensEst
				var tps float64
				if !generationStart.IsZero() {
					elapsed := time.Since(generationStart).Seconds()
					if elapsed > 0 {
						tps = float64(currentCompletionTokensEst) / elapsed
					}
				}
				ui.UpdateStatus(a.Config.Model, globalPromptTokensEst, globalCompletionTokensEst, currentCompletionTokensEst, a.Config.ContextWindowLimit, true, tps)
				ui.DrawStatusBar(ncw, theme)
			}
		}
		close(stopKeyListen)
		<-keyListenDone
		sr.Flush()

		// Fallback if title was never printed
		if parser.activeToolName != "" {
			if !parser.titlePrinted {
				parser.printStreamTitle(ncw, theme)
			}
			if parser.outputBuf.Len() > 0 {
				fmt.Fprint(ncw, parser.outputBuf.String())
				parser.outputBuf.Reset()
			}
		}

		if err := <-streamErrChan; err != nil {
			if ctx.Err() == nil {
				errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
				fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
			}
			return
		}

		if assistantMsg == nil {
			return
		}

		totalCompletionTokens += assistantMsg.CompletionTokens
		totalPromptTokens += assistantMsg.PromptTokens
		totalApiDuration += a.lastGenerationDuration

		*messages = append(*messages, *assistantMsg)
		if sessionID != "" {
			_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
		}

		globalPromptTokens, globalCompletionTokens := a.GetGlobalTokens(*messages, allowlist)

		if totalTokens := globalPromptTokens + globalCompletionTokens; totalTokens >= int(a.Config.CompressionThreshold * float64(a.Config.ContextWindowLimit)) {
			a.compressHistory(ctx, messages, sessionID, theme, w)
		}

		var finalTps float64
		if a.lastGenerationDuration > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / a.lastGenerationDuration.Seconds()
		}

		if len(assistantMsg.ToolCalls) == 0 {
			timePrinted = true
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)

			currentCStr := fmt.Sprintf("%d out", assistantMsg.CompletionTokens)
			if assistantMsg.CompletionTokens >= 1000 {
				currentCStr = fmt.Sprintf("%.1fk out", float64(assistantMsg.CompletionTokens)/1000.0)
			}


			cStyled := style.NewStyle().Foreground(theme.Highlight).Render(currentCStr)
			dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")

			fmt.Fprintln(w)
			if !isNonInteractive {
				ui.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps)
				ui.DrawStatusBar(w, theme)

				if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
					fmt.Fprintf(w, "%s%s%s\n", cStyled, dotStyled, timeStyled)
				} else {
					fmt.Fprintln(w, timeStyled)
				}
			} else {
				// Non-interactive fallback showing Context too
				totalTokens := assistantMsg.PromptTokens + assistantMsg.CompletionTokens
				pct := (float64(totalTokens) / float64(a.Config.ContextWindowLimit)) * 100.0
				totStr := fmt.Sprintf("%d", totalTokens)
				if totalTokens >= 1000 {
					totStr = fmt.Sprintf("%.1fk", float64(totalTokens)/1000.0)
				}
				limitStr := fmt.Sprintf("%d", a.Config.ContextWindowLimit)
				if a.Config.ContextWindowLimit >= 1000 {
					limitStr = fmt.Sprintf("%dk", a.Config.ContextWindowLimit/1000)
				}
				ctxStyled := style.NewStyle().Foreground(theme.Primary).Render(fmt.Sprintf("Context: %s/%s (%.1f%%)", totStr, limitStr, pct))
				if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
					fmt.Fprintf(ncw, "%s%s%s%s%s\n", cStyled, dotStyled, ctxStyled, dotStyled, timeStyled)
				} else {
					fmt.Fprintln(ncw, timeStyled)
				}
			}
			return
		}

		if !isNonInteractive {
			ui.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps)
			ui.DrawStatusBar(ncw, theme)
		}

		type toolExecutionResult struct {
			index  int
			output string
			err    error
			tc     db.ToolCall
		}

		resultsChan := make(chan toolExecutionResult, len(assistantMsg.ToolCalls))
		var wg sync.WaitGroup

		// Ask for approvals sequentially to avoid stdout race conditions
		var approvedBatch []db.ToolCall
		var rejectedBatch []db.ToolCall

		for _, tc := range assistantMsg.ToolCalls {
			if ctx.Err() != nil {
				return
			}

			approved := a.Config.IsAutoApprove()
			if !approved {
				var always bool
				approved, always = ui.AskForApproval(ncw, theme)
				ncw.count++
				if always {
					a.Config.AutoApprove = true
					a.Config.YoloMode = true
				}
			}

			if approved {
				approvedBatch = append(approvedBatch, tc)
			} else {
				rejectedBatch = append(rejectedBatch, tc)
			}
		}

		// If any tools were rejected, we fail-fast immediately (same as before)
		if len(rejectedBatch) > 0 {
			for _, tc := range rejectedBatch {
				toolOutput := "Tool execution rejected by user."
				fmt.Fprintln(ncw, "Tool execution rejected.")
				ui.RenderToolOutput(ncw, toolOutput, true, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, -1)

				*messages = append(*messages, db.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    toolOutput,
				})
				if sessionID != "" {
					_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
				}
			}
			return
		}

		// Execute approved tools in parallel
		if len(approvedBatch) > 0 {
			stopSpinner := make(chan struct{})
			spinnerDone := make(chan struct{})
			go func() {
				defer close(spinnerDone)
				frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
				i := 0
				var toolNames []string
				for _, tc := range approvedBatch {
					toolNames = append(toolNames, tc.Function.Name)
				}
				toolsStr := strings.Join(toolNames, ", ")
				for {
					select {
					case <-stopSpinner:
						return
					default:
						frame := frames[i%len(frames)]
						i++
						fmt.Fprintf(ncw, "\r\x1b[K%s Executing %d tools (%s)...",
							style.NewStyle().Foreground(theme.Secondary).Render(frame),
							len(approvedBatch),
							toolsStr,
						)
						time.Sleep(80 * time.Millisecond)
					}
				}
			}()

			for idx, tc := range approvedBatch {
				wg.Add(1)
				go func(i int, call db.ToolCall) {
					defer wg.Done()

					allowed, reason := a.runBeforeToolHook(call)
					if !allowed {
						resultsChan <- toolExecutionResult{
							index:  i,
							output: fmt.Sprintf("Error: Tool execution blocked by before-hook: %s", reason),
							err:    fmt.Errorf("blocked by hook"),
							tc:     call,
						}
						return
					}

					out, err := a.Registry.Execute(a, call.Function.Name, call.Function.Arguments)

					out, err = a.runAfterToolHook(call, out, err)

					resultsChan <- toolExecutionResult{
						index:  i,
						output: out,
						err:    err,
						tc:     call,
					}
				}(idx, tc)
			}

			wg.Wait()
			close(stopSpinner)
			<-spinnerDone
			fmt.Fprint(ncw, "\r\x1b[K")
			close(resultsChan)

			// Collect results and sort them to preserve call order
			sortedResults := make([]toolExecutionResult, len(approvedBatch))
			for r := range resultsChan {
				sortedResults[r.index] = r
			}

			toolTitleLineNumbers := parser.toolTitleLineNumbers
			for _, r := range sortedResults {
				toolOutput := r.output
				toolErr := r.err
				if toolErr != nil {
					toolOutput = fmt.Sprintf("Error: %v", toolErr)
				}

				if toolOutput == "" {
					toolOutput = "(no output)"
				}

				newlineDist := -1
				if r.index < len(toolTitleLineNumbers) {
					newlineDist = ncw.count - toolTitleLineNumbers[r.index]
				}

				ui.RenderToolOutput(ncw, toolOutput, toolErr != nil, a.Config.CollapseResults, theme, r.tc.Function.Name, r.tc.Function.Arguments, newlineDist)

				// Prefer keeping edit diffs in lastToolOutput over read-only tools output
				isReadOnlyTool := r.tc.Function.Name == "read" || r.tc.Function.Name == "ls" || r.tc.Function.Name == "grep" || r.tc.Function.Name == "find"
				isPrevEdit := a.lastToolWasEdit

				if !isReadOnlyTool || !isPrevEdit || a.lastToolOutput == "" {
					a.lastToolOutput = toolOutput
					a.lastToolIsError = toolErr != nil
					a.lastToolTheme = theme
					a.lastToolWasEdit = (r.tc.Function.Name == "edit")
				}

				*messages = append(*messages, db.Message{
					Role:       "tool",
					ToolCallID: r.tc.ID,
					Name:       r.tc.Function.Name,
					Content:    toolOutput,
				})
				if sessionID != "" {
					_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
				}
			}
		}

		time.Sleep(200 * time.Millisecond)
	}

	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	fmt.Fprintf(w, "\n%s Reached maximum reasoning steps limit (%d).\n", errStyle.Render("WARNING:"), maxSteps)
}

func (a *Agent) compressHistory(
	ctx context.Context,
	messages *[]db.Message,
	sessionID string,
	theme ui.UITheme,
	w io.Writer,
) {
	if len(*messages) <= 12 {
		return
	}

	keepIdx := len(*messages) - 10
	if keepIdx < 1 {
		keepIdx = 1
	}

	toCompress := (*messages)[1:keepIdx]

	var transcriptBuilder strings.Builder
	for _, m := range toCompress {
		if m.Role == "user" {
			transcriptBuilder.WriteString(fmt.Sprintf("User: %s\n\n", m.Content))
		} else if m.Role == "assistant" {
			if m.Content != "" {
				transcriptBuilder.WriteString(fmt.Sprintf("Agent: %s\n\n", m.Content))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					transcriptBuilder.WriteString(fmt.Sprintf("Agent requested tool call: %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
		} else if m.Role == "tool" {
			out := m.Content
			if len(out) > 400 {
				out = out[:400] + "... (truncated)"
			}
			transcriptBuilder.WriteString(fmt.Sprintf("Tool Output (%s): %s\n\n", m.Name, out))
		}
	}

	transcript := transcriptBuilder.String()
	summaryPrompt := fmt.Sprintf(
		"Summarize the following developer-agent conversation transcript, preserving all key actions, decisions, file modifications, tool outputs, and technical findings in a highly concise technical summary. Format as a brief technical log:\n\n%s",
		transcript,
	)

	summaryMsgs := []db.Message{
		{
			Role:    "user",
			Content: summaryPrompt,
		},
	}

	infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, infoStyle.Render("[System: Context usage threshold reached. Compressing older conversation history...]"))

	dummyChan := make(chan StreamChunk, 100)
	go func() {
		for range dummyChan {
			// Discard summarizer stream chunks
		}
	}()

	summaryAssistantMsg, err := a.StreamChatCompletions(ctx, summaryMsgs, []string{}, dummyChan)
	if err != nil {
		warnStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s Failed to compress conversation context: %v\n", warnStyle.Render("WARNING:"), err)
		return
	}

	summaryText := summaryAssistantMsg.Content
	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf("[System: Below is a summary of the earlier conversation history:\n%s]", summaryText),
	}

	newMessages := make([]db.Message, 0, len(*messages))
	newMessages = append(newMessages, (*messages)[0]) // Keep system prompt
	newMessages = append(newMessages, summaryMsg)     // Add summary
	newMessages = append(newMessages, (*messages)[keepIdx:]...) // Add latest messages

	if sessionID != "" {
		_ = db.ClearSession(sessionID)
		for _, msg := range newMessages {
			_ = db.SaveMessage(sessionID, msg)
		}
	}
	*messages = newMessages

	successStyle := style.NewStyle().Foreground(theme.Success).Italic(true)
	fmt.Fprintf(w, "%s Context successfully compressed. Freed %d messages.\n\n", successStyle.Render("✔"), keepIdx-1)
}

type jsonStreamParser struct {
	inString    bool
	inEscape    bool
	currentKey  string
	inValue     bool
	buf         strings.Builder
	isContent   bool
	isPath      bool
	pathPrinted bool

	// Fields for syntax highlighting streamed code
	path        string
	lineBuffer  strings.Builder

	// Fields for diff streaming in edit tool
	isOldText bool
	isNewText bool

	// Field for language auto-detection
	guessedLang string

	// Fields for path-in-title rendering
	activeToolName string
	titlePrinted   bool

	// Output buffer for lazy rendering when path is not yet known
	outputBuf strings.Builder

	needsLeadingNewline bool
	toolTitleLineNumbers []int
	activeToolIndex      int
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit" || p.activeToolName == "ls" || p.activeToolName == "grep" || p.activeToolName == "find"
}

type parserWriter struct {
	p *jsonStreamParser
	w io.Writer
}

func (pw parserWriter) Write(data []byte) (int, error) {
	if pw.p.needsPath() && pw.p.path == "" && !pw.p.titlePrinted {
		return pw.p.outputBuf.Write(data)
	}
	return pw.w.Write(data)
}

func (p *jsonStreamParser) feed(chunk string, w io.Writer, theme ui.UITheme) {
	pw := parserWriter{p: p, w: w}

	for i := 0; i < len(chunk); i++ {
		char := chunk[i]

		if p.inString {
			if p.inEscape {
				p.inEscape = false
				var unescaped string
				switch char {
				case 'n':
					unescaped = "\n"
				case 't':
					unescaped = "\t"
				case 'r':
					unescaped = "\r"
				case '"', '\\', '/':
					unescaped = string(char)
				default:
					unescaped = "\\" + string(char)
				}

				if p.inValue {
					if p.isContent {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(unescaped))
						}
					} else if p.isPath {
						p.path += unescaped
					} else if p.isOldText {
						// Suppress raw oldText from stream output
					} else if p.isNewText {
						// Suppress raw newText from stream output
					}
				} else {
					p.buf.WriteString(unescaped)
				}
			} else if char == '\\' {
				p.inEscape = true
			} else if char == '"' {
				p.inString = false
				strVal := p.buf.String()
				p.buf.Reset()

				if !p.inValue {
					p.currentKey = strVal
				} else {
					if p.isPath {
						if !p.titlePrinted {
							p.pathPrinted = true
							p.printStreamTitle(w, theme)
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						} else if !p.pathPrinted {
							p.pathPrinted = true
							p.updateStreamTitleWithPath(w, theme)
						}
					}
					p.inValue = false
					p.isContent = false
					p.isPath = false
					p.isOldText = false
					p.isNewText = false
				}
			} else {
				if p.inValue {
					charStr := string(char)
					if p.isContent {
						if char == '\n' {
							fmt.Fprint(pw, "\n")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Highlight).Render(charStr))
						}
					} else if p.isPath {
						p.path += charStr
					} else if p.isOldText {
						// Suppress raw oldText from stream output
					} else if p.isNewText {
						// Suppress raw newText from stream output
					}
				} else {
					p.buf.WriteByte(char)
				}
			}
		} else {
			if char == '"' {
				p.inString = true
			} else if char == ':' {
				p.inValue = true
				if p.currentKey == "content" || p.currentKey == "write_content" || p.currentKey == "command" {
					p.isContent = true
					p.guessedLang = ""
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
					// Suppress redundant argument labels (content:, write_content:, command:) to keep streaming clean
					// fmt.Fprintf(pw, "%s:\n", keyStyle.Render(p.currentKey))
				} else if p.currentKey == "path" {
					p.isPath = true
					p.path = ""
					// Do not print "path: " inline
				} else if p.currentKey == "oldText" {
					p.isOldText = true
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
				} else if p.currentKey == "newText" {
					p.isNewText = true
					if !p.titlePrinted {
						p.printStreamTitle(w, theme)
						if p.outputBuf.Len() > 0 {
							fmt.Fprint(w, p.outputBuf.String())
							p.outputBuf.Reset()
						}
					}
				}
			} else if char == ',' || char == '{' || char == '[' {
				p.inValue = false
				p.isContent = false
				p.isPath = false
				p.isOldText = false
				p.isNewText = false
				p.currentKey = ""
			}
		}
	}
}

func guessLanguage(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}

	// Skip comments to find structural keywords
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "<!--") {
		return ""
	}
	if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#!") {
		return ""
	}

	if strings.HasPrefix(trimmed, "#!") {
		if strings.Contains(trimmed, "python") {
			return "python"
		}
		if strings.Contains(trimmed, "bash") || strings.Contains(trimmed, "sh") {
			return "bash"
		}
	}
	if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
		if strings.HasSuffix(trimmed, ";") || strings.Contains(trimmed, "from '") || strings.Contains(trimmed, "from \"") {
			return "javascript"
		}
		if strings.Contains(trimmed, "\"fmt\"") || strings.Contains(trimmed, "package ") {
			return "go"
		}
		return "python"
	}
	if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") {
		return "python"
	}
	if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "func ") {
		return "go"
	}
	if strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "let ") || strings.HasPrefix(trimmed, "function ") {
		return "javascript"
	}
	if strings.HasPrefix(trimmed, "<?php") {
		return "php"
	}
	if strings.HasPrefix(trimmed, "<!") || strings.HasPrefix(trimmed, "<html") {
		return "html"
	}
	return ""
}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	if a.Config.BeforeToolHook == "" {
		return true, ""
	}

	cmd := exec.Command("bash", "-c", a.Config.BeforeToolHook)
	cmd.Env = os.Environ()

	payload := map[string]string{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
		}
		if reason == "" {
			reason = err.Error()
		}
		return false, reason
	}
	return true, ""
}

func (a *Agent) runAfterToolHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	if a.Config.AfterToolHook == "" {
		return output, toolErr
	}

	cmd := exec.Command("bash", "-c", a.Config.AfterToolHook)
	cmd.Env = os.Environ()

	errStr := ""
	if toolErr != nil {
		errStr = toolErr.Error()
	}

	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
		"output":       output,
		"error":        errStr,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	hookOutput := stdout.String()
	if hookOutput != "" {
		return hookOutput, nil
	}
	return output, toolErr
}

func (p *jsonStreamParser) printTitle(w io.Writer, title string) {
	if p.needsLeadingNewline {
		fmt.Fprintln(w)
		p.needsLeadingNewline = false
	}
	fmt.Fprint(w, title+"\n")
}

func (p *jsonStreamParser) printStreamTitle(w io.Writer, theme ui.UITheme) {
	if p.titlePrinted {
		return
	}
	p.titlePrinted = true

	startCount := getNewlineCount(w)
	if p.needsLeadingNewline {
		startCount++
	}
	p.toolTitleLineNumbers = append(p.toolTitleLineNumbers, startCount)

	var dotStr string
	if p.activeToolName == "write" {
		dotStr = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◇")
	} else {
		// Yellow arrow next to streaming title
		dotStyle := style.NewStyle().Foreground(style.Color("#fbbf24")).Bold(true)
		dotStr = dotStyle.Render("▸")
	}

	var title string
	if p.needsPath() && p.path != "" {
		title = ui.FormatToolTitle(dotStr, p.activeToolName, p.path, theme)
	} else {
		title = ui.FormatToolTitle(dotStr, p.activeToolName, "", theme)
	}
	p.printTitle(w, title)
}

func (p *jsonStreamParser) updateStreamTitleWithPath(w io.Writer, theme ui.UITheme) {
	if len(p.toolTitleLineNumbers) == 0 {
		return
	}
	titleLine := p.toolTitleLineNumbers[len(p.toolTitleLineNumbers)-1]
	currentCount := getNewlineCount(w)
	diff := currentCount - titleLine

	_, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && diff >= 0 && diff < height-1 {
		var dotStr string
		if p.activeToolName == "write" {
			dotStr = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◇")
		} else {
			dotStyle := style.NewStyle().Foreground(style.Color("#fbbf24")).Bold(true)
			dotStr = dotStyle.Render("▸")
		}

		newTitle := ui.FormatToolTitle(dotStr, p.activeToolName, p.path, theme)

		// Save cursor position
		fmt.Fprint(w, "\x1b[s")
		// Move up diff lines
		fmt.Fprintf(w, "\x1b[%dA", diff)
		// Move to column 1 absolutely
		fmt.Fprint(w, "\x1b[1G")
		// Overwrite the entire line and clear to the end of the line
		fmt.Fprintf(w, "\x1b[0m%s\x1b[K", newTitle)
		// Restore cursor position
		fmt.Fprint(w, "\x1b[u")
	}
}


func getRelativePath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	return rel
}

func getNewlineCount(w io.Writer) int {
	type countGetter interface {
		GetCount() int
	}
	if cg, ok := w.(countGetter); ok {
		return cg.GetCount()
	}
	if pw, ok := w.(parserWriter); ok {
		return getNewlineCount(pw.w)
	}
	return 0
}

type newlineCounterWriter struct {
	io.Writer
	count int
}

func (n *newlineCounterWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			n.count++
		}
	}
	return n.Writer.Write(p)
}

func (n *newlineCounterWriter) GetCount() int {
	return n.count
}
