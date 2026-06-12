package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
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

				// Render the first frame immediately so it shows up even for sub-millisecond execution
				fmt.Fprintf(ncw, "\r\x1b[K%s Executing %d tools (%s)...",
					style.NewStyle().Foreground(theme.Secondary).Render(frames[0]),
					len(approvedBatch),
					toolsStr,
				)
				i++

				for {
					select {
					case <-stopSpinner:
						return
					default:
						time.Sleep(80 * time.Millisecond)
						select {
						case <-stopSpinner:
							return
						default:
						}
						frame := frames[i%len(frames)]
						i++
						fmt.Fprintf(ncw, "\r\x1b[K%s Executing %d tools (%s)...",
							style.NewStyle().Foreground(theme.Secondary).Render(frame),
							len(approvedBatch),
							toolsStr,
						)
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
