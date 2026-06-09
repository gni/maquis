package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"bidouille/pkg/config"
	"bidouille/pkg/db"
	"bidouille/pkg/ui/style"
)

func AskForApproval(w io.Writer, theme UITheme) (bool, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	var input io.Reader = os.Stdin
	var output io.Writer = os.Stdout
	var fd int = int(os.Stdin.Fd())
	if err == nil {
		defer tty.Close()
		input = tty
		output = tty
		fd = int(tty.Fd())
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	fmt.Fprint(output, promptStyle.Render(" Approve tool execution? [y/N/a (always)]: "))

	isTerm := term.IsTerminal(fd)
	if isTerm {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	buf := make([]byte, 1)
	n, err := input.Read(buf)
	if err != nil || n == 0 {
		fmt.Fprintln(output)
		return false, false
	}

	char := buf[0]
	// Handle Ctrl+C (3) or Esc (27)
	if char == 3 || char == 27 {
		fmt.Fprintln(output, "rejected")
		return false, false
	}

	if char == 'y' || char == 'Y' {
		fmt.Fprintln(output, "y")
		return true, false
	} else if char == 'a' || char == 'A' {
		fmt.Fprintln(output, "always")
		return true, true
	} else {
		if char == '\r' || char == '\n' {
			fmt.Fprintln(output, "n")
		} else {
			fmt.Fprintf(output, "%c\n", char)
		}
		return false, false
	}
}

func RunInteractiveConfig(cfg *config.Config, theme UITheme, rlInput io.Reader, rlOutput io.Writer) (*config.Config, error) {
	reader := bufio.NewReader(rlInput)

	fmt.Fprintln(rlOutput, "\n=== Bidouille Settings Configuration ===")
	fmt.Fprintln(rlOutput, "Press Enter to keep current default value in brackets [].")

	// 1. Endpoint URL
	fmt.Fprintf(rlOutput, "Endpoint URL [%s]: ", cfg.Endpoint)
	val, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	val = strings.TrimSpace(val)
	if val != "" {
		cfg.Endpoint = val
	}

	// 2. Model Name
	fmt.Fprintf(rlOutput, "Model Name [%s]: ", cfg.Model)
	val, err = reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	val = strings.TrimSpace(val)
	if val != "" {
		cfg.Model = val
	}

	// 3. Temperature
	for {
		fmt.Fprintf(rlOutput, "Temperature [%.2f]: ", cfg.Temperature)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(val)
		if val == "" {
			break
		}
		t, err := strconv.ParseFloat(val, 64)
		if err != nil || t < 0.0 || t > 2.0 {
			fmt.Fprintln(rlOutput, "Error: must be a number between 0.0 and 2.0")
			continue
		}
		cfg.Temperature = t
		break
	}

	// 4. Visual Theme
	for {
		fmt.Fprintf(rlOutput, "Visual Theme (dark, neon, light) [%s]: ", cfg.Theme)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			break
		}
		if val != "dark" && val != "neon" && val != "light" {
			fmt.Fprintln(rlOutput, "Error: must be 'dark', 'neon', or 'light'")
			continue
		}
		cfg.Theme = val
		break
	}

	// 5. Auto-Approve Tool Executions
	for {
		fmt.Fprintf(rlOutput, "Auto-Approve Tool Executions? (y/n) [%t]: ", cfg.AutoApprove)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			break
		}
		if val == "y" || val == "yes" || val == "true" || val == "1" {
			cfg.AutoApprove = true
			cfg.YoloMode = true
			break
		} else if val == "n" || val == "no" || val == "false" || val == "0" {
			cfg.AutoApprove = false
			cfg.YoloMode = false
			break
		}
		fmt.Fprintln(rlOutput, "Error: must be 'y' or 'n'")
	}

	// 6. Show Streaming Thinking Process
	for {
		fmt.Fprintf(rlOutput, "Show Streaming Thinking Process? (y/n) [%t]: ", cfg.ShowThinking)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			break
		}
		if val == "y" || val == "yes" || val == "true" || val == "1" {
			cfg.ShowThinking = true
			break
		} else if val == "n" || val == "no" || val == "false" || val == "0" {
			cfg.ShowThinking = false
			break
		}
		fmt.Fprintln(rlOutput, "Error: must be 'y' or 'n'")
	}

	// 7. Collapse Tool Outputs
	for {
		fmt.Fprintf(rlOutput, "Collapse Tool Outputs? (y/n) [%t]: ", cfg.CollapseResults)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			break
		}
		if val == "y" || val == "yes" || val == "true" || val == "1" {
			cfg.CollapseResults = true
			break
		} else if val == "n" || val == "no" || val == "false" || val == "0" {
			cfg.CollapseResults = false
			break
		}
		fmt.Fprintln(rlOutput, "Error: must be 'y' or 'n'")
	}

	// 8. Show Token Usage Metrics
	for {
		fmt.Fprintf(rlOutput, "Show Token Usage Metrics? (y/n) [%t]: ", cfg.ShowTokens)
		val, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			break
		}
		if val == "y" || val == "yes" || val == "true" || val == "1" {
			cfg.ShowTokens = true
			break
		} else if val == "n" || val == "no" || val == "false" || val == "0" {
			cfg.ShowTokens = false
			break
		}
		fmt.Fprintln(rlOutput, "Error: must be 'y' or 'n'")
	}

	return cfg, nil
}

func ChooseSession(w io.Writer, sessions []db.SessionInfo, rlInput io.Reader, rlOutput io.Writer) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found")
	}

	fmt.Fprintln(rlOutput, "\nAvailable Past Sessions:")
	for i, s := range sessions {
		previewText := s.Preview
		if len(previewText) > 40 {
			previewText = previewText[:40] + "..."
		}
		fmt.Fprintf(rlOutput, "  [%d] %s (%d msgs) - %s\n", i+1, s.Timestamp[:16], s.MsgCount, previewText)
	}
	fmt.Fprintln(rlOutput)

	reader := bufio.NewReader(rlInput)
	for {
		fmt.Fprintf(rlOutput, "Enter session number to load (1 to %d) or press Enter to cancel: ", len(sessions))
		choiceStr, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		choiceStr = strings.TrimSpace(choiceStr)
		if choiceStr == "" {
			return "", fmt.Errorf("selection cancelled")
		}

		num, err := strconv.Atoi(choiceStr)
		if err != nil || num < 1 || num > len(sessions) {
			fmt.Fprintln(rlOutput, "Error: invalid session number.")
			continue
		}
		return sessions[num-1].SessionID, nil
	}
}

func RunMultilineEditor(rlInput io.Reader, rlOutput io.Writer) (string, error) {
	fmt.Fprintln(rlOutput, "=== Multiline Prompt Editor ===")
	fmt.Fprintln(rlOutput, "Type or paste your prompt here. When finished, type '.' on a line by itself and press Enter.")
	fmt.Fprintln(rlOutput, "Type 'cancel' to abort.")

	var lines []string
	scanner := bufio.NewScanner(rlInput)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "." {
			break
		}
		if line == "cancel" && len(lines) == 0 {
			return "", fmt.Errorf("multiline editor cancelled")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func RunSessionExplorer(theme UITheme, rlInput io.Reader, rlOutput io.Writer) (string, bool, error) {
	for {
		sessions, err := db.GetSessions()
		if err != nil {
			return "", false, err
		}

		fmt.Fprintln(rlOutput, "\n==================================================")
		fmt.Fprintln(rlOutput, "               BIDOUILLE SESSIONS                 ")
		fmt.Fprintln(rlOutput, "==================================================")
		if len(sessions) == 0 {
			fmt.Fprintln(rlOutput, "No past sessions found.")
		} else {
			for i, s := range sessions {
				preview := s.Preview
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				fmt.Fprintf(rlOutput, "  [%d] %s (%d messages) - %s\n", i+1, s.Timestamp[:16], s.MsgCount, preview)
			}
		}
		fmt.Fprintln(rlOutput, "--------------------------------------------------")
		fmt.Fprintln(rlOutput, "Options:")
		fmt.Fprintln(rlOutput, "  <number>     : Load session")
		fmt.Fprintln(rlOutput, "  d <number>   : Delete session")
		fmt.Fprintln(rlOutput, "  n            : Start a new session")
		fmt.Fprintln(rlOutput, "  q            : Cancel and return")
		fmt.Fprint(rlOutput, "\nChoose option: ")

		reader := bufio.NewReader(rlInput)
		inputStr, err := reader.ReadString('\n')
		if err != nil {
			return "", false, err
		}
		inputStr = strings.TrimSpace(inputStr)

		if inputStr == "" {
			continue
		}
		if inputStr == "q" || inputStr == "exit" {
			return "", false, fmt.Errorf("cancelled")
		}
		if inputStr == "n" {
			return "", true, nil
		}

		if strings.HasPrefix(inputStr, "d ") {
			numStr := strings.TrimSpace(strings.TrimPrefix(inputStr, "d "))
			num, err := strconv.Atoi(numStr)
			if err != nil || num < 1 || num > len(sessions) {
				fmt.Fprintln(rlOutput, "Invalid session number to delete.")
				continue
			}
			sessionID := sessions[num-1].SessionID
			err = db.ClearSession(sessionID)
			if err != nil {
				fmt.Fprintf(rlOutput, "Failed to delete session: %v\n", err)
			} else {
				fmt.Fprintf(rlOutput, "Deleted session %s successfully.\n", sessionID)
			}
			continue
		}

		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(sessions) {
			return sessions[num-1].SessionID, false, nil
		}

		fmt.Fprintln(rlOutput, "Invalid choice. Please try again.")
	}
}
