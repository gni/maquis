package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
	"maquis/pkg/ui/style"

	"maquis/pkg/config"
	"maquis/pkg/db"
)

var (
	currentSpinnerFrame string
	spinnerFrameMu      sync.RWMutex
)

func GetCurrentSpinnerFrame() string {
	spinnerFrameMu.RLock()
	defer spinnerFrameMu.RUnlock()
	return currentSpinnerFrame
}

func unwrapWriter(w io.Writer) io.Writer {
	if uw, ok := w.(interface{ Unwrap() io.Writer }); ok {
		return unwrapWriter(uw.Unwrap())
	}
	return w
}

func (a *Agent) RunAgentLoop(ctx context.Context, w io.Writer, messages *[]db.Message, prompt string, allowlist []string, theme style.UITheme, isNonInteractive bool, sessionID string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	if a.TurnStartTime.IsZero() {
		a.TurnStartTime = time.Now()
	}
	startTime := a.TurnStartTime
	defer func() {
		a.TurnStartTime = time.Time{}
	}()

	writerToUse := w
	rawW := unwrapWriter(writerToUse)
	a.CurrentWriter = writerToUse
	a.CurrentContext = ctx
	defer func() {
		a.CurrentWriter = nil
		a.CurrentContext = nil
	}()
	var activity *turnActivity
	if !isNonInteractive && a.UI != nil {
		activity = newTurnActivity(startTime, defaultTurnActivityInterval, func(frame, text string) {
			spinnerFrameMu.Lock()
			currentSpinnerFrame = frame
			spinnerFrameMu.Unlock()
			a.UI.DrawStatsLine(rawW, theme, frame, text)
		})
		defer activity.Stop()
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
				go func(msg db.Message) { _ = db.SaveMessage(sessionID, msg) }((*messages)[0])
			}
		}
		go func(msg db.Message) { _ = db.SaveMessage(sessionID, msg) }((*messages)[len(*messages)-1])
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	go func() {
		for {
			select {
			case receivedSignal := <-sigChan:
				manager := a.MultiAgentManager
				if receivedSignal == syscall.SIGINT && manager != nil {
					if activeName, ok := manager.ActiveSubagentName(); ok && a.UI != nil {
						decision := a.UI.AskForSubagentCancellation(writerToUse, theme, activeName)
						switch decision {
						case SubagentCancellationContinue:
							fmt.Fprintln(writerToUse, "\n[Subagent cancellation dismissed]")
							continue
						case SubagentCancellationSkipCurrent:
							if manager.CancelSubagentTurn(activeName) {
								fmt.Fprintf(writerToUse, "\n[Skipped subagent: %s]\n", activeName)
							} else {
								fmt.Fprintf(writerToUse, "\n[Subagent already finished: %s]\n", activeName)
							}
							continue
						case SubagentCancellationStopAll:
							manager.CancelAllActiveSubagents()
						}
					} else {
						manager.CancelAllActiveSubagents()
					}
				}
				fmt.Fprintln(writerToUse, "\n\n[Operation Cancelled by User]")
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
		}

		if activity != nil {
			activity.Think()
		}

		if iter > 1 {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
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

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = new(bytes.Buffer)
		a.CurrentStreamMu.Unlock()

		teeWriter := &customTeeWriter{screen: writerToUse, buffer: a.CurrentStreamBuffer}
		ncw := &newlineCounterWriter{Writer: teeWriter}
		var sr StreamRenderer
		if a.UI != nil {
			sr = a.UI.NewStreamRenderer(ncw, theme, a.Config.ShowThinking, a.Config.StreamWrites, "maquis")
		} else {
			sr = &fallbackStreamRenderer{w: ncw}
		}

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
					a.UI.DrawStatusBar(rawW, theme)
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
			a.UI.DrawStatusBar(rawW, theme)
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
						a.UI.DrawStatusBar(rawW, theme)
					}
					lastDraw = now
				}
			}
		}

		sr.Flush()
		stopTicker()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = nil
		a.CurrentStreamMu.Unlock()

		streamErr := <-streamErrChan

		if streamErr != nil {
			if activity != nil {
				activity.Pause()
			}
			if !isNonInteractive {
				fmt.Fprintln(writerToUse)
				cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
				fmt.Fprintln(writerToUse, cancelStyle.Render("[Operation Cancelled]"))
			}
			if ctx.Err() == nil {
				if isNonInteractive {
					errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
					fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), streamErr)
				} else {
					*messages = append(*messages, db.Message{
						Role:    "error",
						Content: streamErr.Error(),
					})
				}
			}
			return
		}

		if assistantMsg == nil {
			return
		}

		totalCompletionTokens += assistantMsg.CompletionTokens
		totalPromptTokens += assistantMsg.PromptTokens
		totalApiDuration += a.lastGenerationDuration

		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()
		*messages = append(*messages, *assistantMsg)
		if sessionID != "" {
			go func(msg db.Message) { _ = db.SaveMessage(sessionID, msg) }((*messages)[len(*messages)-1])
		}

		globalPromptTokens, globalCompletionTokens := a.GetGlobalTokens(*messages, allowlist)
		if totalTokens := globalPromptTokens + globalCompletionTokens; totalTokens >= int(a.Config.CompressionThreshold*float64(a.Config.ContextWindowLimit)) {
			a.compressHistory(ctx, messages, sessionID, theme, writerToUse)
		}

		var finalTps float64
		if a.lastGenerationDuration > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / a.lastGenerationDuration.Seconds()
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if activity != nil {
				activity.Pause()
			}
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

			if !isNonInteractive {
				if a.UI != nil {
					activeTasks := 0
					for _, t := range a.ListTasks() {
						if t.Status == "running" {
							activeTasks++
						}
					}
					a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps, activeTasks, a.Config.ShowTokens)
					a.UI.DrawStatusBar(rawW, theme)
				}
			}
			_, height := getTerminalSize()
			if height > 0 {
				var statsText string
				if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
					statsText = fmt.Sprintf("%s%s%s", cStyled, dotStyled, timeStyled)
				} else {
					statsText = timeStyled
				}
				a.UI.DrawStatsLine(rawW, theme, "", statsText)
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
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		type toolExecutionResult struct {
			index  int
			output string
			err    error
			tc     db.ToolCall
		}

		for idx, tc := range assistantMsg.ToolCalls {
			if ctx.Err() != nil {
				return
			}

			isSubagent := strings.HasPrefix(tc.Function.Name, "subagent__")
			wasStreamed := sr.GetToolTitleLineNumber(idx) != -1

			if !wasStreamed {
				// Render the tool header only if it wasn't already streamed
				if a.UI != nil {
					a.UI.RenderToolHeader(ncw, theme, tc.Function.Name, tc.Function.Arguments)
				} else {
					fmt.Fprintf(ncw, "tool call: %s %s\n", tc.Function.Name, tc.Function.Arguments)
				}
			}

			approved := false
			always := false
			approvalRendered := false
			if !a.Config.AutoApprove && !isReadOnly(tc.Function.Name) {
				approvalRendered = true
				sr.Flush()
				if a.UI != nil {
					if activity != nil {
						activity.Pause()
					}
					approved, always = a.UI.AskForApproval(ncw, theme)
					if activity != nil {
						activity.Think()
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
				allowed, reason := a.runBeforeToolHook(tc)
				var toolOutput string
				var toolErr error

				if !allowed {
					toolOutput = fmt.Sprintf("Error: Tool execution blocked by before-hook: %s", reason)
					toolErr = fmt.Errorf("blocked by hook")
				} else {
					if activity != nil {
						if isSubagent {
							activity.Pause()
						} else {
							activity.Execute(tc.Function.Name)
						}
					}

					toolOutput, toolErr = a.Registry.Execute(a, tc.Function.Name, tc.Function.Arguments)
					toolOutput, toolErr = a.runAfterToolHook(tc, toolOutput, toolErr)

					if activity != nil {
						activity.Think()
					}
				}

				if toolErr != nil {
					toolOutput = FormatToolExecutionFailure(tc.Function.Name, toolOutput, toolErr)
				}
				if toolOutput == "" {
					toolOutput = "(no output)"
				}

				if !isSubagent && !approvalRendered {
					sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
				}

				// Render the tool output
				if !isSubagent {
					if a.UI != nil {
						a.UI.RenderToolOutput(ncw, toolOutput, toolErr != nil, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, sr.DidStreamToolBody(idx))
					} else {
						fmt.Fprintln(ncw, toolOutput)
					}
				}

				// Update agent state
				isReadOnlyTool := tc.Function.Name == "read"
				isPrevEdit := a.lastToolWasEdit
				if !isReadOnlyTool || !isPrevEdit || a.lastToolOutput == "" {
					a.lastToolOutput = toolOutput
					a.lastToolIsError = toolErr != nil
					a.lastToolWasEdit = (tc.Function.Name == "edit")
				}

				// Append message to history
				*messages = append(*messages, db.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    toolOutput,
				})
				if sessionID != "" {
					go func(msg db.Message) { _ = db.SaveMessage(sessionID, msg) }((*messages)[len(*messages)-1])
				}

			} else {
				// Rejected!
				toolOutput := "error: tool execution rejected by user."
				a.lastToolOutput = toolOutput
				a.lastToolIsError = true

				if !approvalRendered {
					sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
				}
				if a.UI != nil {
					a.UI.RenderToolOutput(ncw, toolOutput, true, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, sr.DidStreamToolBody(idx))
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
					go func(msg db.Message) { _ = db.SaveMessage(sessionID, msg) }((*messages)[len(*messages)-1])
				}

				// Abort execution of subsequent tools in the batch
				return
			}
		}
	}

	if activity != nil {
		activity.Pause()
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
func (f *fallbackStreamRenderer) WriteReasoning(content string)                    {}
func (f *fallbackStreamRenderer) Flush()                                           {}
func (f *fallbackStreamRenderer) HasOutput() bool                                  { return false }
func (f *fallbackStreamRenderer) StartToolCall(toolName string, toolCallIndex int) {}
func (f *fallbackStreamRenderer) WriteToolCall(content string)                     {}
func (f *fallbackStreamRenderer) GetToolTitleLineNumber(index int) int             { return -1 }
func (f *fallbackStreamRenderer) DidStreamToolBody(index int) bool                 { return false }
func (f *fallbackStreamRenderer) CompleteToolCall(index int, toolName string, toolArgs string, isError bool) {
}
func (f *fallbackStreamRenderer) GetReasoningDuration() float64 { return 0 }

func isReadOnly(toolName string) bool {
	return toolName == "read" || toolName == "task_status"
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

type customTeeWriter struct {
	screen io.Writer
	buffer io.Writer
}

func (c *customTeeWriter) Write(p []byte) (n int, err error) {
	n, err = c.screen.Write(p)
	if err == nil {
		_, _ = c.buffer.Write(p)
	}
	return n, err
}

func (c *customTeeWriter) Unwrap() io.Writer {
	return c.screen
}
