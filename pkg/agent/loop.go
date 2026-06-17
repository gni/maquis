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

	"maquis/pkg/ui/style"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"maquis/pkg/config"
	"maquis/pkg/db"
)

var (
	currentSpinnerFrame string
	spinnerFrameMu      sync.RWMutex
)

func (a *Agent) RunAgentLoop(w io.Writer, messages *[]db.Message, prompt string, allowlist []string, theme style.UITheme, isNonInteractive bool, sessionID string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	startTime := time.Now()
	writerToUse := w
	a.CurrentWriter = writerToUse
	defer func() {
		a.CurrentWriter = nil
	}()
	var height int
	var pauseThinkingSpinner chan bool
	if !isNonInteractive && a.UI != nil {
		stopThinkingSpinner := make(chan struct{})
		thinkingSpinnerDone := make(chan struct{})
		pauseThinkingSpinner = make(chan bool, 1)

		go func() {
			defer close(thinkingSpinnerDone)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			frames := []string{"◜", "◝", "◞", "◟"} // Curved Arcs (Sleek rotating circle)

			i := 0
			paused := false
			for {
				select {
				case <-stopThinkingSpinner:
					return
				case p := <-pauseThinkingSpinner:
					paused = p
					if paused {
						spinnerFrameMu.Lock()
						currentSpinnerFrame = ""
						spinnerFrameMu.Unlock()
						a.UI.DrawStatsLine(w, theme, "", "")
					}
				case <-ticker.C:
					if paused {
						continue
					}
					frame := frames[i%len(frames)]
					i++
					spinnerFrameMu.Lock()
					currentSpinnerFrame = frame
					spinnerFrameMu.Unlock()
					elapsed := time.Since(startTime).Seconds()
					a.UI.DrawStatsLine(w, theme, frame, fmt.Sprintf("(%.1fs)", elapsed))
				}
			}
		}()

		defer func() {
			close(stopThinkingSpinner)
			<-thinkingSpinnerDone
			spinnerFrameMu.Lock()
			currentSpinnerFrame = ""
			spinnerFrameMu.Unlock()
			if a.UI != nil {
				a.UI.DrawStatsLine(w, theme, "", "")
			}
		}()

		fd := int(os.Stderr.Fd())
		if term.IsTerminal(fd) {
			if _, h, err := term.GetSize(fd); err == nil && h > 0 {
				height = h
				writerToUse = newPromptPreservingWriter(w, height)
				a.CurrentWriter = writerToUse
			}
		}

		if height > 0 {
			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K%s\x1b[%d;3H", height-2, promptStyle.Render("> "), height-2)
		}

		// 2. Redraw status bar and static prompt separator
		a.UI.DrawStatusBar(w, theme)
		a.UI.DrawPromptSeparator(w, a.Config.ShowThinking, a.Config.ReasoningEffort, theme, "")

		// 3. Print the prompt separator and user prompt inside the scroll region so they scroll up
		printPromptSeparator(writerToUse, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		fmt.Fprintf(writerToUse, "%s%s\n", promptStyle.Render("> "), prompt)

		divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
		fmt.Fprintln(writerToUse, divider)
	}

	var totalCompletionTokens int
	var totalPromptTokens int
	var totalApiDuration time.Duration

	timePrinted := false
	defer func() {
		if !timePrinted && prompt != "" {
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)
			fmt.Fprintln(writerToUse)
			fmt.Fprintln(writerToUse, timeStyled)
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
			fmt.Fprintln(writerToUse, "\n\n[Operation Cancelled by User]")
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

		if !isNonInteractive && pauseThinkingSpinner != nil {
			select {
			case pauseThinkingSpinner <- false:
			default:
			}
		}

		if iter > 1 {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(writerToUse)
			fmt.Fprintln(writerToUse, divider)
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

		ncw := &newlineCounterWriter{Writer: writerToUse}
		var sr StreamRenderer
		if a.UI != nil {
			sr = a.UI.NewStreamRenderer(ncw, theme, a.Config.ShowThinking, a.Config.StreamWrites)
		} else {
			sr = &fallbackStreamRenderer{w: ncw}
		}

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
					n, err := unix.Read(fd, buf)
					if err == nil && n > 0 {
						if buf[0] == 15 { // Ctrl+O
							runningTaskId := a.GetLastRunningTaskId()
							if runningTaskId != "" {
								a.ToggleStreaming(runningTaskId, writerToUse)
							} else {
								a.Config.CollapseResults = !a.Config.CollapseResults
								_ = config.SaveConfig(a.ConfigPath, a.Config)
								if a.UI != nil {
									a.UI.SetCollapseStatus(a.Config.CollapseResults)
									a.UI.DrawStatusBar(w, theme)
								}
							}
						} else if buf[0] == 20 { // Ctrl+T
							a.Config.ShowThinking = !a.Config.ShowThinking
							_ = config.SaveConfig(a.ConfigPath, a.Config)
							if a.UI != nil {
								spinnerFrameMu.RLock()
								frame := currentSpinnerFrame
								spinnerFrameMu.RUnlock()
								a.UI.DrawPromptSeparator(w, a.Config.ShowThinking, a.Config.ReasoningEffort, theme, frame)
							}
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
							if a.UI != nil {
								spinnerFrameMu.RLock()
								frame := currentSpinnerFrame
								spinnerFrameMu.RUnlock()
								a.UI.DrawPromptSeparator(w, a.Config.ShowThinking, a.Config.ReasoningEffort, theme, frame)
							}
						} else if buf[0] == 3 || buf[0] == 27 { // Ctrl+C or Escape
							fmt.Fprintln(writerToUse, "\n\n[Operation Cancelled by User]")
							cancel()
							return
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}()

		globalPromptTokensEst, priorCompletionTokens := a.GetGlobalTokens(*messages, allowlist)

		tickerDone := make(chan struct{})
		var tickerOnce sync.Once
		stopTicker := func() {
			tickerOnce.Do(func() {
				close(tickerDone)
				if a.UI != nil && !isNonInteractive {
					activeTasks := 0
					for _, t := range a.ListTasks() {
						if t.Status == "running" {
							activeTasks++
						}
					}
					a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, activeTasks, a.Config.ShowTokens)
					a.UI.DrawStatusBar(w, theme)
				}
			})
		}
		defer stopTicker()

		if a.UI != nil && !isNonInteractive {
			activeTasks := 0
			for _, t := range a.ListTasks() {
				if t.Status == "running" {
					activeTasks++
				}
			}
			a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, a.Config.ContextWindowLimit, true, 0, activeTasks, a.Config.ShowTokens)
			a.UI.DrawStatusBar(w, theme)
		}

		var reasoningChars int
		var textChars int
		var toolCallChars int
		var generationStart time.Time

		var lastDraw time.Time
		for chunk := range chunkChan {
			if chunk.Type == "reasoning" {
				if generationStart.IsZero() {
					generationStart = time.Now()
				}
				sr.WriteReasoning(chunk.Content)
				reasoningChars += len(chunk.Content)
			} else {
				if chunk.Type == "text" {
					if generationStart.IsZero() {
						generationStart = time.Now()
					}
					sr.Write(chunk.Content)
					textChars += len(chunk.Content)
				} else if chunk.Type == "tool_name" {
					sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
				} else if chunk.Type == "tool_call" {
					sr.WriteToolCall(chunk.Content)
					toolCallChars += len(chunk.Content)
				}
			}

			if !isNonInteractive {
				now := time.Now()
				if lastDraw.IsZero() || now.Sub(lastDraw) >= 200*time.Millisecond {
					currentCompletionTokensEst := (reasoningChars + textChars + toolCallChars) / 4
					globalCompletionTokensEst := priorCompletionTokens + currentCompletionTokensEst
					var tps float64
					if !generationStart.IsZero() {
						elapsed := now.Sub(generationStart).Seconds()
						if elapsed > 0 {
							tps = float64(currentCompletionTokensEst) / elapsed
						}
					}
					if a.UI != nil {
						activeTasks := 0
						for _, t := range a.ListTasks() {
							if t.Status == "running" {
								activeTasks++
							}
						}
						a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, globalCompletionTokensEst, currentCompletionTokensEst, a.Config.ContextWindowLimit, true, tps, activeTasks, a.Config.ShowTokens)
						a.UI.DrawStatusBar(w, theme)
					}
					lastDraw = now
				}
			}
		}
		close(stopKeyListen)
		<-keyListenDone
		sr.Flush()
		stopTicker()

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
			a.compressHistory(ctx, messages, sessionID, theme, writerToUse)
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

			fmt.Fprintln(writerToUse)
			if !isNonInteractive {
				if a.UI != nil {
					activeTasks := 0
					for _, t := range a.ListTasks() {
						if t.Status == "running" {
							activeTasks++
						}
					}
					a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps, activeTasks, a.Config.ShowTokens)
					a.UI.DrawStatusBar(w, theme)
				}
			}
			if height > 0 {
				var statsText string
				if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
					statsText = fmt.Sprintf("%s%s%s", cStyled, dotStyled, timeStyled)
				} else {
					statsText = timeStyled
				}
				a.UI.DrawStatsLine(w, theme, "", statsText)
			} else {
				if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
					fmt.Fprintf(writerToUse, "%s%s%s\n", cStyled, dotStyled, timeStyled)
				} else {
					fmt.Fprintln(writerToUse, timeStyled)
				}
			}
			return
		}

		if !isNonInteractive {
			if a.UI != nil {
				activeTasks := 0
				for _, t := range a.ListTasks() {
					if t.Status == "running" {
						activeTasks++
					}
				}
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps, activeTasks, a.Config.ShowTokens)
				a.UI.DrawStatusBar(w, theme)
			}
		}

		type toolExecutionResult struct {
			index  int
			output string
			err    error
			tc     db.ToolCall
		}

		resultsChan := make(chan toolExecutionResult, len(assistantMsg.ToolCalls))
		var wg sync.WaitGroup

		var approvedBatch []db.ToolCall
		var rejectedBatch []db.ToolCall

		for _, tc := range assistantMsg.ToolCalls {
			if ctx.Err() != nil {
				return
			}

			approved := false
			always := false
			if !a.Config.AutoApprove && !isReadOnly(tc.Function.Name) {
				sr.Flush()
				if a.UI != nil {
					if !isNonInteractive && pauseThinkingSpinner != nil {
						pauseThinkingSpinner <- true
					}
					approved, always = a.UI.AskForApproval(ncw, theme)
					if !isNonInteractive && pauseThinkingSpinner != nil {
						pauseThinkingSpinner <- false
					}
				} else {
					approved = true
				}
				if always {
					a.Config.AutoApprove = true
					_ = config.SaveConfig(a.ConfigPath, a.Config)
				}
			} else {
				approved = true
			}

			if approved {
				approvedBatch = append(approvedBatch, tc)
			} else {
				rejectedBatch = append(rejectedBatch, tc)
			}
		}

		// Clear all streamed titles and any prompts printed after them
		firstTitleLine := sr.GetToolTitleLineNumber(0)
		if firstTitleLine != -1 && ncw.count > firstTitleLine {
			linesToClear := ncw.count - firstTitleLine
			// Move up, then for each line, clear it and move down
			var clearCmd strings.Builder
			clearCmd.WriteString(fmt.Sprintf("\x1b[%dA\r", linesToClear))
			for i := 0; i < linesToClear; i++ {
				clearCmd.WriteString("\x1b[K\x1b[1B")
			}
			// Move back up to the starting line where we want to place the cursor
			clearCmd.WriteString(fmt.Sprintf("\x1b[%dA\r", linesToClear))
			fmt.Fprint(ncw, clearCmd.String())
			ncw.count = firstTitleLine
			ncw.col = 0
		}

		if len(rejectedBatch) > 0 {
			for _, tc := range rejectedBatch {
				toolOutput := "Error: Tool execution rejected by user."
				a.lastToolOutput = toolOutput
				a.lastToolIsError = true
				if a.UI != nil {
					a.UI.RenderToolOutput(ncw, toolOutput, true, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, -2)
				} else {
					fmt.Fprintln(ncw, toolOutput)
				}

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

		if len(approvedBatch) > 0 {
			if !isNonInteractive && pauseThinkingSpinner != nil {
				pauseThinkingSpinner <- true
			}

			hasSubagentTool := false
			for _, tc := range approvedBatch {
				if strings.HasPrefix(tc.Function.Name, "subagent__") {
					hasSubagentTool = true
					break
				}
			}

			var stopSpinner chan struct{}
			var spinnerDone chan struct{}

			if !hasSubagentTool {
				stopSpinner = make(chan struct{})
				spinnerDone = make(chan struct{})
				go func() {
					defer close(spinnerDone)
					frames := []string{"◜", "◝", "◞", "◟"}
					i := 0
					var toolNames []string
					for _, tc := range approvedBatch {
						toolNames = append(toolNames, tc.Function.Name)
					}
					toolsStr := strings.Join(toolNames, ", ")

					if a.UI != nil {
						a.UI.DrawStatsLine(w, theme, frames[0], fmt.Sprintf("executing %d tools (%s)...", len(approvedBatch), toolsStr))
					} else {
						fmt.Fprintf(ncw, "\r\x1b[K%s executing %d tools (%s)...",
							style.NewStyle().Foreground(theme.Secondary).Render(frames[0]),
							len(approvedBatch),
							toolsStr,
						)
					}
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
							if a.UI != nil {
								a.UI.DrawStatsLine(w, theme, frame, fmt.Sprintf("executing %d tools (%s)...", len(approvedBatch), toolsStr))
							} else {
								fmt.Fprintf(ncw, "\r\x1b[K%s executing %d tools (%s)...",
									style.NewStyle().Foreground(theme.Secondary).Render(frame),
									len(approvedBatch),
									toolsStr,
								)
							}
						}
					}
				}()
			}

			// Pre-calculate which read tool calls are allowed concurrently (maximum 10 read calls in parallel)
			allowedReadIndices := make(map[int]bool)
			readCount := 0
			for i, tc := range approvedBatch {
				if tc.Function.Name == "read" {
					readCount++
					if readCount <= 10 {
						allowedReadIndices[i] = true
					}
				} else {
					allowedReadIndices[i] = true
				}
			}

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

					var out string
					var err error
					if !allowedReadIndices[i] {
						out = "Error: Too many parallel read tool calls in a single turn. Please inspect files one by one or in small batches (maximum 10 at once) to avoid UI clutter and context window overflow."
						err = fmt.Errorf("too many parallel read calls")
					} else {
						out, err = a.Registry.Execute(a, call.Function.Name, call.Function.Arguments)
					}
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
			if !hasSubagentTool {
				close(stopSpinner)
				<-spinnerDone
				if a.UI != nil {
					a.UI.DrawStatsLine(w, theme, "", "")
				} else {
					fmt.Fprint(ncw, "\r\x1b[K")
				}
			}
			close(resultsChan)

			sortedResults := make([]toolExecutionResult, len(approvedBatch))
			for r := range resultsChan {
				sortedResults[r.index] = r
			}

			for _, r := range sortedResults {
				toolOutput := r.output
				toolErr := r.err
				if toolErr != nil {
					toolOutput = fmt.Sprintf("Error: %v", toolErr)
				}

				if toolOutput == "" {
					toolOutput = "(no output)"
				}

				if a.UI != nil {
					a.UI.RenderToolOutput(ncw, toolOutput, toolErr != nil, a.Config.CollapseResults, theme, r.tc.Function.Name, r.tc.Function.Arguments, -2)
				} else {
					fmt.Fprintln(ncw, toolOutput)
				}

				isReadOnlyTool := r.tc.Function.Name == "read" || r.tc.Function.Name == "ls" || r.tc.Function.Name == "grep" || r.tc.Function.Name == "find"
				isPrevEdit := a.lastToolWasEdit

				if !isReadOnlyTool || !isPrevEdit || a.lastToolOutput == "" {
					a.lastToolOutput = toolOutput
					a.lastToolIsError = toolErr != nil
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

			if !isNonInteractive && pauseThinkingSpinner != nil {
				pauseThinkingSpinner <- false
			}
		}

		time.Sleep(200 * time.Millisecond)
	}

	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	fmt.Fprintf(writerToUse, "\n%s reached maximum reasoning steps limit (%d).\n", errStyle.Render("warning:"), maxSteps)
}

type newlineCounterWriter struct {
	io.Writer
	count int
	col   int
	inEsc bool
	inCSI bool
}

func (n *newlineCounterWriter) Write(p []byte) (int, error) {
	termW, _ := getTerminalSize()
	if termW <= 0 {
		termW = 80
	}

	for _, b := range p {
		if n.inEsc {
			if b == '[' {
				n.inCSI = true
				n.inEsc = false
			} else {
				n.inEsc = false
			}
			continue
		}
		if b == '\x1b' {
			n.inEsc = true
			continue
		}
		if n.inCSI {
			if b >= 0x40 && b <= 0x7E {
				n.inCSI = false
			}
			continue
		}

		if b == '\n' {
			n.count++
			n.col = 0
		} else if b == '\r' {
			n.col = 0
		} else if (b >= 32 && b < 127) || b >= 0xC0 {
			n.col++
			if n.col >= termW {
				n.count++
				n.col = 0
			}
		}
	}
	return n.Writer.Write(p)
}

func (n *newlineCounterWriter) GetCount() int {
	return n.count
}

func (n *newlineCounterWriter) Unwrap() io.Writer {
	return n.Writer
}

type fallbackStreamRenderer struct {
	w io.Writer
}

func (f *fallbackStreamRenderer) Write(content string) {
	fmt.Fprint(f.w, content)
}
func (f *fallbackStreamRenderer) WriteReasoning(content string)                     {}
func (f *fallbackStreamRenderer) Flush()                                          {}
func (f *fallbackStreamRenderer) HasOutput() bool                                 { return false }
func (f *fallbackStreamRenderer) StartToolCall(toolName string, toolCallIndex int) {}
func (f *fallbackStreamRenderer) WriteToolCall(content string)                     {}
func (f *fallbackStreamRenderer) GetToolTitleLineNumber(index int) int             { return -1 }

func isReadOnly(toolName string) bool {
	return toolName == "read" || toolName == "ls" || toolName == "grep" || toolName == "find" || toolName == "task_status"
}

func getTerminalSize() (int, int) {
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stderr.Fd())); err == nil && h > 0 {
		return w, h
	}
	return 80, 24
}

func printPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	thinkingText := "off"
	if showThinking {
		thinkingText = reasoningEffort
	}
	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)

	prefix := "─── prompt "
	width, _ := getTerminalSize()
	statusLen := len(stripAnsiSeqs(statusPart))
	prefixLen := len(prefix)

	dashesCount := width - prefixLen - statusLen - 2
	if dashesCount < 3 {
		dashesCount = 3
	}
	dashes := strings.Repeat("─", dashesCount)
	fmt.Fprintf(w, "%s%s\n", borderStyle.Render(prefix+dashes), statusStyle.Render(statusPart))
}

