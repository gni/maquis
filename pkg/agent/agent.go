package agent

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"bidouille/pkg/ui/style"
	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui"
)

var (
	lastToolOutput  string
	lastToolIsError bool
	lastToolTheme   ui.UITheme
)

func autoCompleteCallback(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' {
		return line, pos, false
	}

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
		"/exit",
		"/quit",
		"/multiline",
		"/paste",
		"/mcp",
	}

	var matches []string
	for _, c := range candidates {
		if strings.HasPrefix(c, line[:pos]) {
			matches = append(matches, c)
		}
	}

	if len(matches) == 0 {
		return line, pos, false
	}

	if len(matches) == 1 {
		completed := matches[0] + line[pos:]
		return completed, len(matches[0]), true
	}

	prefix := matches[0]
	for _, m := range matches[1:] {
		for i := 0; i < len(prefix) && i < len(m); i++ {
			if prefix[i] != m[i] {
				prefix = prefix[:i]
				break
			}
		}
		if len(prefix) > len(m) {
			prefix = m
		}
	}

	if len(prefix) > len(line[:pos]) {
		completed := prefix + line[pos:]
		return completed, len(prefix), true
	}

	return line, pos, false
}

type historyReader struct {
	historyBuf *bytes.Reader
	realStdin  io.Reader
	realStdout io.Writer
	muted      bool
}

func (h *historyReader) Read(p []byte) (n int, err error) {
	if h.historyBuf.Len() > 0 {
		return h.historyBuf.Read(p)
	}
	return h.realStdin.Read(p)
}

func (h *historyReader) Write(p []byte) (n int, err error) {
	if h.muted {
		return len(p), nil
	}
	return h.realStdout.Write(p)
}

func RunREPL(cfg *config.Config, configPath string, httpClient *http.Client, allowedTools []string, theme ui.UITheme, initialSessionID string) {
	currentSessionID := initialSessionID
	if currentSessionID == "" {
		currentSessionID = db.NewUUID()
	}

	var exitMessage string
	var messages []db.Message
	if dbHistory, err := db.LoadMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
		exitMessage = fmt.Sprintf("Loaded past session %s (%d messages)", currentSessionID, len(messages))
	} else {
		messages = []db.Message{
			{Role: "system", Content: GetSystemPrompt(cfg)},
		}
		if initialSessionID != "" {
			exitMessage = fmt.Sprintf("Initialized brand new session %s", currentSessionID)
		} else {
			exitMessage = fmt.Sprintf("Started new session %s", currentSessionID)
		}
	}

	ui.PrintBanner(os.Stderr, cfg)
	fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("─── Prompt ───────────────────────────────────────────────"))

	fd := int(os.Stdin.Fd())

	// Load command history
	historyLines, _ := db.GetUserHistory()
	var historyData strings.Builder
	for _, hLine := range historyLines {
		hLine = strings.ReplaceAll(hLine, "\n", " ")
		historyData.WriteString(hLine + "\n")
	}

	hr := &historyReader{
		historyBuf: bytes.NewReader([]byte(historyData.String())),
		realStdin:  os.Stdin,
		realStdout: os.Stdout,
		muted:      true,
	}

	rl := term.NewTerminal(hr, "")
	rl.AutoCompleteCallback = autoCompleteCallback

	// Pre-seed history in the term.Terminal by reading all history lines while muted
	for i := 0; i < len(historyLines); i++ {
		_, err := rl.ReadLine()
		if err != nil {
			break
		}
	}
	hr.muted = false

	for {
		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		promptStr := promptStyle.Render("> ")
		rl.SetPrompt(promptStr)

		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Printf("Error setting terminal raw mode: %v\n", err)
			os.Exit(1)
		}

		line, err := rl.ReadLine()
		term.Restore(fd, oldState)

		if err != nil {
			break
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("──────────────────────────────────────────────────────────"))

		if strings.HasPrefix(line, "!") {
			cmdStr := strings.TrimSpace(strings.TrimPrefix(line, "!"))
			if cmdStr == "" {
				fmt.Fprintln(os.Stderr, "Usage: !<command>")
				continue
			}

			fmt.Fprintf(os.Stderr, "Executing: %s\n", cmdStr)

			cmd := exec.Command("bash", "-c", cmdStr)
			cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
			cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
			cmd.Stdin = os.Stdin
			err := cmd.Run()

			output := sanitizeUTF8(stdout.Bytes())
			errOutput := sanitizeUTF8(stderr.Bytes())

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
			fmt.Fprintln(os.Stderr, successStyle.Render("\nCommand output appended to conversation context."))
			fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("─── Prompt ───────────────────────────────────────────────"))
			continue
		}

		if handled, quit := HandleSlashCommand(line, cfg, configPath, httpClient, &messages, allowedTools, &theme, os.Stderr, &currentSessionID); handled {
			if quit {
				break
			}
			fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("─── Prompt ───────────────────────────────────────────────"))
			continue
		}

		RunAgentLoop(os.Stderr, cfg, configPath, httpClient, &messages, line, allowedTools, theme, false, currentSessionID)
		fmt.Fprintln(os.Stderr, style.NewStyle().Foreground(theme.Border).Render("─── Prompt ───────────────────────────────────────────────"))
	}

	var finalStatus string
	if currentSessionID == initialSessionID {
		finalStatus = exitMessage
	} else {
		if len(messages) > 1 {
			finalStatus = fmt.Sprintf("Session %s", currentSessionID)
		} else {
			finalStatus = fmt.Sprintf("Initialized brand new session %s", currentSessionID)
		}
	}
	fmt.Fprintf(os.Stderr, "Goodbye! %s.\n", finalStatus)
}


