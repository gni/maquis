package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"bidouille/pkg/ui/style"
	"github.com/alecthomas/chroma/v2/quick"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
)


func RunAgentLoop(w io.Writer, cfg *config.Config, configPath string, client *http.Client, messages *[]db.Message, prompt string, allowlist []string, theme ui.UITheme, isNonInteractive bool, sessionID string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

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

	maxSteps := cfg.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 9999
	}
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
		}

		chunkChan := make(chan StreamChunk, 200)
		streamErrChan := make(chan error, 1)
		var assistantMsg *db.Message

		go func() {
			msg, err := StreamChatCompletions(ctx, cfg, client, *messages, allowlist, chunkChan)
			streamErrChan <- err
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
		}()

		sr := ui.NewStreamRenderer(w, theme, cfg.ShowThinking)

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
							cfg.CollapseResults = !cfg.CollapseResults
							_ = config.SaveConfig(configPath, cfg)
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

		parser := &jsonStreamParser{}

		for chunk := range chunkChan {
			if chunk.Type == "reasoning" {
				sr.WriteReasoning(chunk.Content)
			} else if chunk.Type == "text" {
				sr.Write(chunk.Content)
			} else if chunk.Type == "tool_name" {
				parser.activeToolName = chunk.Content
			} else if chunk.Type == "tool_call" {
				parser.feed(chunk.Content, w, theme)
			}
		}
		close(stopKeyListen)
		<-keyListenDone
		sr.Flush()

		// Fallback if title was never printed
		if parser.activeToolName != "" {
			if !parser.titlePrinted {
				parser.titlePrinted = true
				titleStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
				if parser.path != "" {
					fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s %s", parser.activeToolName, parser.path)))
				} else {
					fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s", parser.activeToolName)))
				}
			}
			if parser.outputBuf.Len() > 0 {
				fmt.Fprint(w, parser.outputBuf.String())
				parser.outputBuf.Reset()
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)

		if err := <-streamErrChan; err != nil {
			if ctx.Err() == nil {
				errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
				fmt.Fprintf(w, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
			}
			return
		}

		if assistantMsg == nil {
			return
		}

		*messages = append(*messages, *assistantMsg)
		if sessionID != "" {
			_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
		}

		totalTokens := assistantMsg.PromptTokens + assistantMsg.CompletionTokens
		pct := (float64(totalTokens) / float64(cfg.ContextWindowLimit)) * 100.0

		pStr := fmt.Sprintf("%d in", assistantMsg.PromptTokens)
		if assistantMsg.PromptTokens >= 1000 {
			pStr = fmt.Sprintf("%.1fk in", float64(assistantMsg.PromptTokens)/1000.0)
		}
		cStr := fmt.Sprintf("%d out", assistantMsg.CompletionTokens)
		if assistantMsg.CompletionTokens >= 1000 {
			cStr = fmt.Sprintf("%.1fk out", float64(assistantMsg.CompletionTokens)/1000.0)
		}

		totStr := fmt.Sprintf("%d", totalTokens)
		if totalTokens >= 1000 {
			totStr = fmt.Sprintf("%.1fk", float64(totalTokens)/1000.0)
		}
		limitStr := fmt.Sprintf("%d", cfg.ContextWindowLimit)
		if cfg.ContextWindowLimit >= 1000 {
			limitStr = fmt.Sprintf("%dk", cfg.ContextWindowLimit/1000)
		}

		pStyled := style.NewStyle().Foreground(theme.Secondary).Render(pStr)
		cStyled := style.NewStyle().Foreground(theme.Highlight).Render(cStr)
		ctxStyled := style.NewStyle().Foreground(theme.Primary).Render(fmt.Sprintf("Context: %s/%s (%.1f%%)", totStr, limitStr, pct))
		dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")

		if cfg.ShowTokens && (assistantMsg.PromptTokens > 0 || assistantMsg.CompletionTokens > 0) {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "%s%s%s%s%s\n", pStyled, dotStyled, cStyled, dotStyled, ctxStyled)
		}

		if totalTokens >= int(cfg.CompressionThreshold * float64(cfg.ContextWindowLimit)) {
			compressHistory(ctx, cfg, client, messages, sessionID, theme, w)
		}

		if len(assistantMsg.ToolCalls) == 0 {
			return
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

			approved := cfg.IsAutoApprove()
			if !approved {
				var always bool
				approved, always = ui.AskForApproval(w, theme)
				if always {
					cfg.AutoApprove = true
					cfg.YoloMode = true
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
				fmt.Fprintln(w, "Tool execution rejected.")
				ui.RenderToolOutput(w, toolOutput, true, cfg.CollapseResults, theme)

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
						fmt.Fprintf(w, "\r\x1b[K%s Executing %d tools (%s)...",
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

					allowed, reason := runBeforeToolHook(cfg, call)
					if !allowed {
						resultsChan <- toolExecutionResult{
							index:  i,
							output: fmt.Sprintf("Error: Tool execution blocked by before-hook: %s", reason),
							err:    fmt.Errorf("blocked by hook"),
							tc:     call,
						}
						return
					}

					out, err := ExecuteTool(call.Function.Name, call.Function.Arguments)

					out, err = runAfterToolHook(cfg, call, out, err)

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
			fmt.Fprint(w, "\r\x1b[K")
			close(resultsChan)

			// Collect results and sort them to preserve call order
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

				ui.RenderToolOutput(w, toolOutput, toolErr != nil, cfg.CollapseResults, theme)

				lastToolOutput = toolOutput
				lastToolIsError = toolErr != nil
				lastToolTheme = theme

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

func compressHistory(
	ctx context.Context,
	cfg *config.Config,
	client *http.Client,
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
	fmt.Fprintln(w, infoStyle.Render("\n[System: Context usage threshold reached. Compressing older conversation history...]"))

	dummyChan := make(chan StreamChunk, 100)
	go func() {
		for range dummyChan {
			// Discard summarizer stream chunks
		}
	}()

	summaryAssistantMsg, err := StreamChatCompletions(ctx, cfg, client, summaryMsgs, []string{}, dummyChan)
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
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit"
}

type parserWriter struct {
	p *jsonStreamParser
	w io.Writer
}

func (pw parserWriter) Write(data []byte) (int, error) {
	if pw.p.needsPath() && pw.p.path == "" {
		return pw.p.outputBuf.Write(data)
	}
	return pw.w.Write(data)
}

func (p *jsonStreamParser) feed(chunk string, w io.Writer, theme ui.UITheme) {
	keyStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	valStyle := style.NewStyle().Foreground(theme.Text)
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
							line := p.lineBuffer.String()
							p.lineBuffer.Reset()
							
							if p.guessedLang == "" || p.guessedLang == "plaintext" {
								p.guessedLang = guessLanguage(line)
							}

							lang := "plaintext"
							if p.currentKey == "command" {
								lang = "bash"
							} else if p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									lang = strings.ToLower(ext[1:])
								}
							} else if p.guessedLang != "" {
								lang = p.guessedLang
							}
							fmt.Fprint(pw, "\r\x1b[K")
							_ = quick.Highlight(pw, line+"\n", lang, "terminal16", "friendly")
						} else {
							p.lineBuffer.WriteString(unescaped)
							
							if p.guessedLang == "" || p.guessedLang == "plaintext" {
								p.guessedLang = guessLanguage(p.lineBuffer.String())
							}

							fmt.Fprint(pw, "\r\x1b[K")
							lang := "plaintext"
							if p.currentKey == "command" {
								lang = "bash"
							} else if p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									lang = strings.ToLower(ext[1:])
								}
							} else if p.guessedLang != "" {
								lang = p.guessedLang
							}
							_ = quick.Highlight(pw, p.lineBuffer.String(), lang, "terminal16", "friendly")
						}
					} else if p.isPath {
						p.path += unescaped
						if p.titlePrinted && !p.pathPrinted {
							fmt.Fprint(pw, valStyle.Render(unescaped))
						}
					} else if p.isOldText {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n      ")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Error).Render(unescaped))
						}
					} else if p.isNewText {
						if unescaped == "\n" {
							fmt.Fprint(pw, "\n      ")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Success).Render(unescaped))
						}
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
						p.path = strVal
						if !p.titlePrinted {
							p.titlePrinted = true
							p.pathPrinted = true
							titleStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
							fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s %s", p.activeToolName, p.path)))
							if p.outputBuf.Len() > 0 {
								fmt.Fprint(w, p.outputBuf.String())
								p.outputBuf.Reset()
							}
						}
					}
					if p.isContent && p.lineBuffer.Len() > 0 {
						line := p.lineBuffer.String()
						p.lineBuffer.Reset()
						lang := "plaintext"
						if p.currentKey == "command" {
							lang = "bash"
						} else if p.path != "" {
							ext := filepath.Ext(p.path)
							if len(ext) > 1 {
								lang = strings.ToLower(ext[1:])
							}
						} else if p.guessedLang != "" {
							lang = p.guessedLang
						}
						fmt.Fprint(pw, "\r\x1b[K")
						_ = quick.Highlight(pw, line+"\n", lang, "terminal16", "friendly")
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
							line := p.lineBuffer.String()
							p.lineBuffer.Reset()
							
							if p.guessedLang == "" || p.guessedLang == "plaintext" {
								p.guessedLang = guessLanguage(line)
							}

							lang := "plaintext"
							if p.currentKey == "command" {
								lang = "bash"
							} else if p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									lang = strings.ToLower(ext[1:])
								}
							} else if p.guessedLang != "" {
								lang = p.guessedLang
							}
							fmt.Fprint(pw, "\r\x1b[K")
							_ = quick.Highlight(pw, line+"\n", lang, "terminal16", "friendly")
						} else {
							p.lineBuffer.WriteByte(char)
							
							if p.guessedLang == "" || p.guessedLang == "plaintext" {
								p.guessedLang = guessLanguage(p.lineBuffer.String())
							}

							fmt.Fprint(pw, "\r\x1b[K")
							lang := "plaintext"
							if p.currentKey == "command" {
								lang = "bash"
							} else if p.path != "" {
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									lang = strings.ToLower(ext[1:])
								}
							} else if p.guessedLang != "" {
								lang = p.guessedLang
							}
							_ = quick.Highlight(pw, p.lineBuffer.String(), lang, "terminal16", "friendly")
						}
					} else if p.isPath {
						p.path += charStr
						if p.titlePrinted && !p.pathPrinted {
							fmt.Fprint(pw, valStyle.Render(charStr))
						}
					} else if p.isOldText {
						if char == '\n' {
							fmt.Fprint(pw, "\n      ")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Error).Render(charStr))
						}
					} else if p.isNewText {
						if char == '\n' {
							fmt.Fprint(pw, "\n      ")
						} else {
							fmt.Fprint(pw, style.NewStyle().Foreground(theme.Success).Render(charStr))
						}
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
						if p.needsPath() {
							// Wait for path
						} else {
							p.titlePrinted = true
							titleStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
							fmt.Fprintf(w, "\n%s\n", titleStyle.Render(fmt.Sprintf("Tool Call: %s", p.activeToolName)))
						}
					}
					fmt.Fprintf(pw, "\n  %s:\n", keyStyle.Render(p.currentKey))
				} else if p.currentKey == "path" {
					p.isPath = true
					p.path = ""
					if p.titlePrinted && !p.pathPrinted {
						fmt.Fprintf(pw, "  %s: ", keyStyle.Render("path"))
					}
				} else if p.currentKey == "oldText" {
					p.isOldText = true
					fmt.Fprintf(pw, "\n    %s:\n      ", style.NewStyle().Foreground(theme.Error).Render("- [Old text]"))
				} else if p.currentKey == "newText" {
					p.isNewText = true
					fmt.Fprintf(pw, "\n    %s:\n      ", style.NewStyle().Foreground(theme.Success).Render("+ [New text]"))
				}
			} else if char == ',' {
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

func runBeforeToolHook(cfg *config.Config, tc db.ToolCall) (bool, string) {
	if cfg.BeforeToolHook == "" {
		return true, ""
	}

	cmd := exec.Command("bash", "-c", cfg.BeforeToolHook)
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

func runAfterToolHook(cfg *config.Config, tc db.ToolCall, output string, toolErr error) (string, error) {
	if cfg.AfterToolHook == "" {
		return output, toolErr
	}

	cmd := exec.Command("bash", "-c", cfg.AfterToolHook)
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
