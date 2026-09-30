package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

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

	var loader *turnLoader
	if a.UI != nil && !isNonInteractive {
		loader = a.newTurnLoader(ctx, rawW, theme, startTime)
		defer loader.Stop()
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
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
		sr.SetPrompt(prompt)

		globalPromptTokensEst, _ := a.GetGlobalTokens(*messages, allowlist)
		priorCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		tickerDone := make(chan struct{})
		var tickerOnce sync.Once
		stopTicker := func() {
			tickerOnce.Do(func() {
				close(tickerDone)
				if a.UI != nil && !isNonInteractive {
					a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, a.Config.ContextWindowLimit, false, 0, a.CountActiveTasks(), a.Config.ShowTokens)
					a.UI.DrawStatusBar(rawW, theme)
				}
			})
		}
		defer stopTicker()

		if a.UI != nil && !isNonInteractive {
			a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, a.Config.ContextWindowLimit, true, 0, a.CountActiveTasks(), a.Config.ShowTokens)
			a.UI.DrawStatusBar(rawW, theme)
		}

		for chunk := range chunkChan {
			if chunk.Type == "reasoning" {
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				sr.WriteReasoning(chunk.Content)
			} else if chunk.Type == "text" {
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				sr.Write(chunk.Content)
			} else if chunk.Type == "tool_name" {
				if chunk.ToolCallIndex == 0 {
					sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
				}
				if loader != nil {
					loader.ShowDots()
					loader.Feed()
				}
			} else if chunk.Type == "tool_call" {
				if chunk.ToolCallIndex == 0 {
					sr.WriteToolCall(chunk.Content)
				}
				if sr.DidStreamToolBody(chunk.ToolCallIndex) {
					if loader != nil {
						loader.PauseDots()
						loader.Feed()
					}
				} else if loader != nil {
					loader.Feed()
				}
			}
		}
		if loader != nil {
			loader.ShowDots()
		}

		sr.Flush()
		stopTicker()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = nil
		a.CurrentStreamMu.Unlock()

		streamErr := <-streamErrChan

		if streamErr != nil {
			if loader != nil {
				loader.Stop()
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

		if prompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, prompt)
		}

		totalCompletionTokens += assistantMsg.CompletionTokens
		totalPromptTokens += assistantMsg.PromptTokens
		totalApiDuration += a.lastGenerationDuration

		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()
		*messages = append(*messages, *assistantMsg)
		if sessionID != "" {
			_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
		}

		globalPromptTokens, _ := a.GetGlobalTokens(*messages, allowlist)
		globalCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)
		if totalTokens := globalPromptTokens + assistantMsg.CompletionTokens; totalTokens >= int(a.Config.CompressionThreshold*float64(a.Config.ContextWindowLimit)) {
			a.compressHistory(ctx, messages, sessionID, theme, writerToUse)
		}

		var finalTps float64
		if a.lastGenerationDuration > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / a.lastGenerationDuration.Seconds()
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if loader != nil {
				loader.Stop()
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

			var statsText string
			if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
				statsText = fmt.Sprintf("%s%s%s", cStyled, dotStyled, timeStyled)
			} else {
				statsText = timeStyled
			}

			_, height := getTerminalSize()
			if height > 0 {
				a.UI.DrawStatsLine(rawW, theme, "", statsText)
			} else {
				fmt.Fprintln(writerToUse, statsText)
			}

			if !isNonInteractive {
				if a.UI != nil {
					a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps, a.CountActiveTasks(), a.Config.ShowTokens)
					a.UI.DrawStatusBar(rawW, theme)
				}
			}
			return
		}

		if !isNonInteractive {
			if a.UI != nil {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, a.Config.ContextWindowLimit, false, finalTps, a.CountActiveTasks(), a.Config.ShowTokens)
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

			if !wasStreamed || (len(assistantMsg.ToolCalls) > 1 && idx > 0) {
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
					if loader != nil {
						loader.Pause()
					}
					approved, always = a.UI.AskForApproval(ncw, theme)
					if loader != nil {
						loader.Resume()
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
					if loader != nil {
						loader.ShowDots()
					}
					toolOutput, toolErr = a.Registry.Execute(a, tc.Function.Name, tc.Function.Arguments)
					toolOutput, toolErr = a.runAfterToolHook(tc, toolOutput, toolErr)
				}

				if toolErr != nil {
					toolOutput = FormatToolExecutionFailure(tc.Function.Name, toolOutput, toolErr)
				}
				if toolOutput == "" {
					toolOutput = "(no output)"
				}

				if !isSubagent && !approvalRendered && len(assistantMsg.ToolCalls) == 1 {
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
					_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
				}

			} else {
				// Rejected!
				toolOutput := "error: tool execution rejected by user."
				a.lastToolOutput = toolOutput
				a.lastToolIsError = true

				if !approvalRendered && len(assistantMsg.ToolCalls) == 1 {
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
					_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
				}

				// Abort execution of subsequent tools in the batch
				return
			}
		}
	}

	if loader != nil {
		loader.Stop()
	}
	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	fmt.Fprintf(writerToUse, "\n%s reached maximum reasoning steps limit (%d).\n", errStyle.Render("warning:"), maxSteps)
}

type turnLoader struct {
	agent     *Agent
	w         io.Writer
	theme     style.UITheme
	startTime time.Time
	done      chan struct{}
	stopOnce  sync.Once

	mu         sync.Mutex
	hasDots    bool
	paused     bool
	frameIndex int
	lastFeed   time.Time
}

func (a *Agent) newTurnLoader(ctx context.Context, w io.Writer, theme style.UITheme, startTime time.Time) *turnLoader {
	if a.UI == nil {
		return nil
	}
	if startTime.IsZero() {
		startTime = time.Now()
	}
	tl := &turnLoader{
		agent:     a,
		w:         w,
		theme:     theme,
		startTime: startTime,
		done:      make(chan struct{}),
		hasDots:   true,
		lastFeed:  time.Now(),
	}
	go tl.run(ctx)
	return tl
}

func (tl *turnLoader) currentFrame() string {
	activeDot := style.NewStyle().Foreground(tl.theme.Highlight).Bold(true).Render("•")
	mutedDot := style.NewStyle().Foreground(tl.theme.Border).Render("·")
	frames := []string{
		fmt.Sprintf("%s %s %s", activeDot, mutedDot, mutedDot),
		fmt.Sprintf("%s %s %s", mutedDot, activeDot, mutedDot),
		fmt.Sprintf("%s %s %s", mutedDot, mutedDot, activeDot),
		fmt.Sprintf("%s %s %s", mutedDot, activeDot, mutedDot),
	}
	return frames[tl.frameIndex%len(frames)]
}

func (tl *turnLoader) renderFrame(frame string) {
	select {
	case <-tl.done:
		return
	default:
	}
	elapsed := time.Since(tl.startTime).Seconds()
	timeStr := fmt.Sprintf("(%.1fs)", elapsed)
	timeStyled := style.NewStyle().Foreground(tl.theme.Border).Render(timeStr)
	tl.agent.UI.DrawStatsLine(tl.w, tl.theme, frame, timeStyled)
}

func (tl *turnLoader) run(ctx context.Context) {
	ticker := time.NewTicker(240 * time.Millisecond)
	defer ticker.Stop()

	tl.mu.Lock()
	initFrame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(initFrame)

	for {
		select {
		case <-tl.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			tl.mu.Lock()
			if tl.paused {
				tl.mu.Unlock()
				continue
			}
			// If dots were paused (e.g. streaming) but no chunks received for 500ms, stream has hung/paused -> resume dots
			if !tl.hasDots && time.Since(tl.lastFeed) > 500*time.Millisecond {
				tl.hasDots = true
			}
			frame := ""
			if tl.hasDots {
				frame = tl.currentFrame()
				tl.frameIndex++
			}
			tl.mu.Unlock()

			tl.renderFrame(frame)
		}
	}
}

func (tl *turnLoader) PauseDots() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.hasDots = false
	tl.lastFeed = time.Now()
	tl.mu.Unlock()
	tl.renderFrame("")
}

func (tl *turnLoader) ShowDots() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.hasDots = true
	tl.lastFeed = time.Now()
	frame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(frame)
}

func (tl *turnLoader) Feed() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.lastFeed = time.Now()
	tl.mu.Unlock()
}

func (tl *turnLoader) Pause() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.paused = true
	tl.mu.Unlock()
	tl.agent.UI.DrawStatsLine(tl.w, tl.theme, "", "")
}

func (tl *turnLoader) Resume() {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.paused = false
	tl.hasDots = true
	tl.lastFeed = time.Now()
	frame := tl.currentFrame()
	tl.frameIndex++
	tl.mu.Unlock()
	tl.renderFrame(frame)
}

func (tl *turnLoader) Stop() {
	if tl == nil {
		return
	}
	tl.stopOnce.Do(func() {
		close(tl.done)
		tl.agent.UI.DrawStatsLine(tl.w, tl.theme, "", "")
	})
}

func (a *Agent) startSoftDotLoader(ctx context.Context, w io.Writer, theme style.UITheme) func() {
	if a.UI == nil {
		return func() {}
	}
	tl := a.newTurnLoader(ctx, w, theme, a.TurnStartTime)
	return func() {
		if tl != nil {
			tl.Stop()
		}
	}
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
func (f *fallbackStreamRenderer) SetPrompt(prompt string)       {}

func isReadOnly(toolName string) bool {
	return toolName == "read" || toolName == "task_status"
}

func getTerminalSize() (int, int) {
	return style.GetTerminalSize()
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
