package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"maquis/pkg/ui/style"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"github.com/alecthomas/chroma/v2/quick"
)

var TerminalMu = &agent.TerminalMu

func PrintBanner(w io.Writer, a *agent.Agent) {
	if a == nil || a.Config == nil {
		return
	}
	cfg := a.Config
	theme := GetConfiguredTheme(cfg)

	iconStyle := style.NewStyle().
		Foreground(theme.Secondary).
		MarginLeft(2)

	infoStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		MarginLeft(4)

	icon := `
  /\___/\
 (  o.o  )
 (  =^=  )
  \_____/`

	pluginsCount := 0
	if a.Registry != nil {
		for name := range a.Registry.GetAllExecutors() {
			if strings.HasPrefix(name, "plugin__") {
				pluginsCount++
			}
		}
	}

	extensionsCount := 0
	var dirs []string
	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".maquis", "extensions"))
	}
	dirs = append(dirs, filepath.Join(a.GetWorkspaceRoot(), "extensions"))

	seen := make(map[string]bool)
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err == nil && info.Mode()&0111 != 0 {
				base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				cmdName := "/" + strings.ToLower(base)
				if !seen[cmdName] {
					seen[cmdName] = true
					extensionsCount++
				}
			}
		}
	}

	pluginWord := "plugin"
	if pluginsCount != 1 {
		pluginWord = "plugins"
	}
	extWord := "extension"
	if extensionsCount != 1 {
		extWord = "extensions"
	}
	tagStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)
	tagStr := tagStyle.Render(fmt.Sprintf("[%d %s, %d %s]", pluginsCount, pluginWord, extensionsCount, extWord))

	info := fmt.Sprintf("\n\nmaquis v1.0.0  %s\nendpoint: %s\nmodel:    %s", tagStr, cfg.Endpoint, cfg.Model)

	joined := style.JoinHorizontal(
		style.Center,
		iconStyle.Render(icon),
		infoStyle.Render(info),
	)

	fmt.Fprintln(w, joined)
	fmt.Fprintln(w)
}

func RenderHelp(w io.Writer, theme UITheme) {
	headerStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Underline(true)

	cmdStyle := style.NewStyle().
		Foreground(theme.Secondary).
		Bold(true)

	descStyle := style.NewStyle().
		Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("slash commands reference:"))
	fmt.Fprintln(w)

	commands := [][]string{
		{"/config", "open interactive settings editor"},
		{"/config show", "display current settings summary"},
		{"/config set <key> <value>", "modify settings dynamically"},
		{"/session <list/new/load/clear>", "manage persistent chat sessions interactively"},
		{"/skills", "list all available reference skills"},
		{"/skills load <name>", "explicitly load a reference skill into the context"},
		{"/mcp", "list all configured mcp servers and active tool schemas"},
		{"/plugins", "list all registered custom plugin tools"},
		{"/extensions", "list all custom slash command extensions"},
		{"/reload", "re-scan and hot-reload custom plugins and tools"},
		{"/agent <list/join/spawn/skill/kill>", "manage multi-agent swarm threads interactively"},
		{"/rewind", "clear conversation history"},
		{"/help, /commands, ?", "display this help menu"},
		{"/exit, /quit", "exit the maquis CLI application"},
	}

	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render(cmd[0]), descStyle.Render(cmd[1]))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, headerStyle.Render("keyboard shortcuts:"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("Ctrl+O"), descStyle.Render("toggle tool results collapsing (collapsed vs. full)"))
	fmt.Fprintln(w)

	fmt.Fprintln(w, headerStyle.Render("local command execution:"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("!<command>"), descStyle.Render("execute any local shell command (e.g. !git diff)"))
	fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render("<direct_command>"), descStyle.Render("execute common utilities directly (if enabled)"))
	fmt.Fprintln(w, "  supported direct utilities: ls, pwd, git, cat, cd")
	fmt.Fprintln(w)
}

func RenderConfig(w io.Writer, cfg *config.Config, theme UITheme) {
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	keyStyle := style.NewStyle().
		Foreground(theme.Secondary)

	valStyle := style.NewStyle().
		Foreground(theme.Text)

	approveVal := "disabled (interactive mode)"
	if cfg.AutoApprove {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("enabled (auto-approve)")
	}

	directVal := "disabled"
	if cfg.DirectCommands {
		directVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("enabled")
	}

	configStr := fmt.Sprintf(
		"%s\n\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %.2f\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %d tokens\n"+
			"  %-20s %d\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n\n"+
			"tip: change any setting via: /config <key> <value> (e.g. /config yes true)",
		titleStyle.Render("maquis runtime settings"),
		keyStyle.Render("endpoint:"), valStyle.Render(cfg.Endpoint),
		keyStyle.Render("model:"), valStyle.Render(cfg.Model),
		keyStyle.Render("temperature:"), cfg.Temperature,
		keyStyle.Render("auto-approve:"), approveVal,
		keyStyle.Render("show thinking:"), cfg.ShowThinking,
		keyStyle.Render("collapse results:"), cfg.CollapseResults,
		keyStyle.Render("show tokens:"), cfg.ShowTokens,
		keyStyle.Render("context limit:"), cfg.ContextWindowLimit,
		keyStyle.Render("max reasoning steps:"), cfg.MaxReasoningSteps,
		keyStyle.Render("direct commands:"), directVal,
		keyStyle.Render("client cert:"), valStyle.Render(cfg.CertFile),
		keyStyle.Render("client key:"), valStyle.Render(cfg.KeyFile),
		keyStyle.Render("skip ssl verify:"), valStyle.Render(fmt.Sprintf("%v", cfg.SkipVerify)),
		keyStyle.Render("stream writes:"), cfg.StreamWrites,
		keyStyle.Render("visual theme:"), valStyle.Render(cfg.Theme),
		keyStyle.Render("syntax theme:"), valStyle.Render(cfg.SyntaxTheme),
	)

	fmt.Fprintln(w, borderStyle.Render(configStr))
}

func formatToolArguments(toolName string, argsJSON string, theme UITheme) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return "arguments: " + argsJSON
	}

	keyStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	valStyle := style.NewStyle().Foreground(theme.Text)

	var sb strings.Builder

	// We separate "simple" arguments (e.g., path, limit) from "block" arguments (e.g., content, command, edits)
	var simpleLines []string
	var blockLines []string

	// Order keys: put 'path' first if it exists
	keys := make([]string, 0, len(m))
	if _, ok := m["path"]; ok {
		keys = append(keys, "path")
	}
	for k := range m {
		if k != "path" {
			keys = append(keys, k)
		}
	}

	for _, k := range keys {
		v := m[k]
		switch k {
		case "content", "command":
			strVal, ok := v.(string)
			if !ok {
				strVal = fmt.Sprintf("%v", v)
			}

			lang := "plaintext"
			if k == "command" {
				lang = "bash"
			} else if pathVal, ok := m["path"].(string); ok {
				ext := filepath.Ext(pathVal)
				if len(ext) > 1 {
					lang = ext[1:]
				}
			}

			var codeBuf bytes.Buffer
			err := quick.Highlight(&codeBuf, strVal, lang, "terminal16", "friendly")
			var highlightedStr string
			if err == nil {
				highlightedStr = codeBuf.String()
			} else {
				highlightedStr = strVal
			}

			// Pretty format multi-line text
			var blockSb strings.Builder
			blockSb.WriteString(fmt.Sprintf("%s:\n", keyStyle.Render(k)))
			lines := strings.Split(highlightedStr, "\n")
			for i, line := range lines {
				// Don't print empty trailing line if it's the last one
				if line == "" && i == len(lines)-1 {
					continue
				}
				blockSb.WriteString(fmt.Sprintf("%s\n", line))
			}
			blockLines = append(blockLines, strings.TrimSuffix(blockSb.String(), "\n"))

		case "edits", "updates":
			// Parse edits/updates list
			edits, ok := v.([]interface{})
			if !ok {
				continue
			}
			var blockSb strings.Builder
			blockSb.WriteString(fmt.Sprintf("%s:\n", keyStyle.Render(k)))
			for i, eVal := range edits {
				eMap, ok := eVal.(map[string]interface{})
				if !ok {
					continue
				}
				oldText, _ := eMap["oldText"].(string)
				newText, _ := eMap["newText"].(string)

				blockSb.WriteString(fmt.Sprintf("edit block %d:\n", i+1))
				if oldText != "" {
					blockSb.WriteString(style.NewStyle().Foreground(theme.Error).Render("- [old text]:\n"))
					for _, line := range strings.Split(oldText, "\n") {
						blockSb.WriteString(fmt.Sprintf("%s\n", style.NewStyle().Foreground(theme.Error).Render(line)))
					}
				}
				if newText != "" {
					blockSb.WriteString(style.NewStyle().Foreground(theme.Success).Render("+ [new text]:\n"))
					for _, line := range strings.Split(newText, "\n") {
						blockSb.WriteString(fmt.Sprintf("%s\n", style.NewStyle().Foreground(theme.Success).Render(line)))
					}
				}
			}
			blockLines = append(blockLines, strings.TrimSuffix(blockSb.String(), "\n"))

		default:
			// Normal inline key value
			valStr := fmt.Sprintf("%v", v)
			simpleLines = append(simpleLines, fmt.Sprintf("%s: %s", keyStyle.Render(k), valStyle.Render(valStr)))
		}
	}

	if len(simpleLines) > 0 {
		sb.WriteString(strings.Join(simpleLines, "\n"))
	}
	if len(blockLines) > 0 {
		if len(simpleLines) > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.Join(blockLines, "\n\n"))
	}

	return sb.String()
}

func RenderToolCall(w io.Writer, toolName string, arguments string, theme UITheme) {
	titleStyle := style.NewStyle().
		Foreground(theme.Secondary).
		Bold(true)

	formattedArgs := formatToolArguments(toolName, arguments, theme)

	fmt.Fprintln(w, titleStyle.Render(fmt.Sprintf("tool call: %s", toolName)))
	fmt.Fprint(w, formattedArgs)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme UITheme, toolName string, argsJSON string, newlineCount int) {
	if newlineCount >= 0 {
		_, height := getTerminalSize()
		if newlineCount < height-1 {
			var finalDot string
			if toolName == "write" {
				if !isError {
					finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("◆")
				} else {
					finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("◆")
				}
			} else {
				if !isError {
					finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("▸")
				} else {
					finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("▸")
				}
			}

			// Extract path or command/pattern from argsJSON if possible
			pathVal := ""
			var argsMap map[string]interface{}
			if argsJSON != "" && json.Unmarshal([]byte(argsJSON), &argsMap) == nil {
				if p, ok := argsMap["path"].(string); ok && p != "" {
					pathVal = p
				} else if c, ok := argsMap["command"].(string); ok && c != "" {
					pathVal = c
				} else if pat, ok := argsMap["pattern"].(string); ok && pat != "" {
					pathVal = pat
				}
			}

			finalTitle := FormatToolTitle(finalDot, toolName, pathVal, theme)

			// Update the tool title in-place in a single Write call to avoid breaking the cursor restoring writer
			fmt.Fprintf(w, "\x1b7\x1b[%dA\x1b[1G\x1b[0m%s\x1b[K\x1b8", newlineCount, finalTitle)
		}
	}

	if newlineCount == -2 {
		var finalDot string
		if toolName == "write" {
			if !isError {
				finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("◆")
			} else {
				finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("◆")
			}
		} else {
			if !isError {
				finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("▸")
			} else {
				finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("▸")
			}
		}

		pathVal := ""
		var argsMap map[string]interface{}
		if argsJSON != "" && json.Unmarshal([]byte(argsJSON), &argsMap) == nil {
			if p, ok := argsMap["path"].(string); ok && p != "" {
				pathVal = p
			} else if c, ok := argsMap["command"].(string); ok && c != "" {
				pathVal = c
			} else if pat, ok := argsMap["pattern"].(string); ok && pat != "" {
				pathVal = pat
			}
		}

		finalTitle := FormatToolTitle(finalDot, toolName, pathVal, theme)
		fmt.Fprintln(w, finalTitle)
	}

	if collapse && !isError {
		return
	}

	fmt.Fprintln(w) // Print a leading blank line to separate from the tool call or previous elements
	borderColor := theme.Success
	title := "tool output"
	if isError {
		borderColor = theme.Error
		title = "tool error"
	}

	titleStyle := style.NewStyle().
		Foreground(borderColor).
		Bold(true)

	bodyStyle := style.NewStyle().
		Foreground(theme.Text)

	body := output
	isJSON := false
	if !isError {
		trimmedBody := strings.TrimSpace(body)
		if (strings.HasPrefix(trimmedBody, "{") && strings.HasSuffix(trimmedBody, "}")) || (strings.HasPrefix(trimmedBody, "[") && strings.HasSuffix(trimmedBody, "]")) {
			var temp interface{}
			if json.Unmarshal([]byte(trimmedBody), &temp) == nil {
				if pretty, err := json.MarshalIndent(temp, "", "  "); err == nil {
					body = string(pretty)
					isJSON = true
				}
			}
		}
	}

	if !isError && (toolName == "read" || toolName == "write" || isJSON) {
		lang := "plaintext"
		if isJSON {
			lang = "json"
		} else {
			var args struct {
				Path         string `json:"path"`
				Content      string `json:"content"`
				WriteContent string `json:"write_content"`
			}
			if argsJSON != "" {
				_ = json.Unmarshal([]byte(argsJSON), &args)
				if args.Content == "" && args.WriteContent != "" {
					args.Content = args.WriteContent
				}
				if args.Path != "" {
					ext := filepath.Ext(args.Path)
					if len(ext) > 1 {
						lang = ext[1:]
					}
				}
			}

			if toolName == "write" && args.Content != "" {
				body = args.Content
			}
		}

		chromaStyle := theme.ChromaStyle
		if chromaStyle == "" {
			chromaStyle = "friendly"
		}
		var codeBuf bytes.Buffer
		err := quick.Highlight(&codeBuf, body, lang, "terminal16", chromaStyle)
		if err == nil {
			body = codeBuf.String()
		}
	}

	isQuietTitle := !isError

	if !isQuietTitle {
		fmt.Fprintln(w, titleStyle.Render(title+":"))
	}

	body = strings.TrimRight(body, "\r\n")
	lines := strings.Split(body, "\n")
	printLine := func(line string) {
		var renderedLine string
		if strings.Contains(line, "\x1b[") {
			renderedLine = line
		} else {
			renderedLine = bodyStyle.Render(line)
		}
		fmt.Fprintln(w, renderedLine)
	}

	if collapse && len(lines) > 8 {
		for i := 0; i < 8; i++ {
			printLine(lines[i])
		}
		collapsedCount := len(lines) - 8
		collapsedMsg := fmt.Sprintf("  ... [%d lines collapsed. Press Ctrl+O or type /expand to view full output] ...", collapsedCount)
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(collapsedMsg))
	} else {
		for _, line := range lines {
			printLine(line)
		}
	}
}

func wrapText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= limit {
			currentLine.WriteByte(' ')
			currentLine.WriteString(word)
		} else {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}
	return lines
}

func RenderMCPStartupErrors(w io.Writer, startErrors map[string]error, theme UITheme) {
	if len(startErrors) == 0 {
		return
	}

	titleStyle := style.NewStyle().
		Foreground(theme.Error).
		Bold(true)

	msgStyle := style.NewStyle().
		Foreground(theme.Text)

	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Error).
		Padding(0, 1).
		Margin(1, 0)

	// Determine available width
	termW, _ := getTerminalSize()

	// 4 columns reserved for border & padding
	availWidth := termW - 4
	if availWidth < 20 {
		availWidth = 20
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("failed to start mcp server(s):") + "\n")
	for name, err := range startErrors {
		prefix := fmt.Sprintf("  - %s: ", name)
		prefixLen := len(prefix)
		prefixStyled := fmt.Sprintf("  - %s: ", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name))

		errAvailWidth := availWidth - prefixLen
		if errAvailWidth < 15 {
			errAvailWidth = 15
		}

		errLines := wrapText(err.Error(), errAvailWidth)
		for idx, line := range errLines {
			if idx == 0 {
				sb.WriteString(prefixStyled + msgStyle.Render(line) + "\n")
			} else {
				padding := strings.Repeat(" ", prefixLen)
				sb.WriteString(padding + msgStyle.Render(line) + "\n")
			}
		}
	}

	fmt.Fprint(w, borderStyle.Render(strings.TrimSuffix(sb.String(), "\n")))
	fmt.Fprintln(w)
}

func FormatToolTitle(symbol string, toolName string, path string, theme UITheme) string {
	toolStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
	pathStyle := style.NewStyle().Foreground(style.Color("#ffffff"))

	if path != "" {
		relPath := path
		if toolName != "bash" {
			wd, err := os.Getwd()
			if err == nil {
				if rel, err := filepath.Rel(wd, path); err == nil {
					relPath = rel
				}
			}
		}
		return fmt.Sprintf("%s %s %s", symbol, toolStyle.Render(toolName), pathStyle.Render(relPath))
	}
	return fmt.Sprintf("%s %s", symbol, toolStyle.Render(toolName))
}

func PrintPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	PrintPromptSeparatorWithSpinner(w, showThinking, reasoningEffort, theme, "")
}

func PrintPromptSeparatorWithSpinner(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	thinkingText := "off"
	if showThinking {
		thinkingText = reasoningEffort
	}
	
	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
	prefixCombined := borderStyle.Render("─── prompt ")

	width, _ := getTerminalSize()
	statusLen := utf8.RuneCountInString(stripAnsi(statusPart))
	prefixLen := 11 // visual column length of "─── prompt " is always 11 columns

	dashesCount := width - prefixLen - statusLen - 2
	if dashesCount < 3 {
		dashesCount = 3
	}
	dashes := strings.Repeat("─", dashesCount)
	fmt.Fprintf(w, "%s%s\n", prefixCombined+borderStyle.Render(dashes), statusStyle.Render(statusPart))
}

func getWriterHeight(w io.Writer) int {
	type heightGetter interface {
		Height() int
	}
	type wrapper interface {
		Unwrap() io.Writer
	}

	curr := w
	for curr != nil {
		if hg, ok := curr.(heightGetter); ok {
			return hg.Height()
		}
		if wr, ok := curr.(wrapper); ok {
			curr = wr.Unwrap()
		} else {
			break
		}
	}
	return 0
}

func DrawStaticStatsLine(w io.Writer, theme UITheme, spinnerFrame string, statsText string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	height := getWriterHeight(w)
	if height <= 0 {
		_, h := getTerminalSize()
		height = h
	}

	stateMu.Lock()
	if spinnerFrame == "" {
		if statsText != "" {
			lastStatsText = statsText
		}
	}
	textToDraw := statsText
	if textToDraw == "" && spinnerFrame == "" {
		textToDraw = lastStatsText
	}
	stateMu.Unlock()

	var buf bytes.Buffer
	// Move cursor to height-4-pasteLinesOffset absolutely, clear line
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", height-4-pasteLinesOffset)

	if spinnerFrame != "" {
		spinnerStyled := style.NewStyle().Foreground(theme.Primary).Bold(true).Render(spinnerFrame)
		if textToDraw != "" {
			fmt.Fprintf(&buf, "%s %s", spinnerStyled, textToDraw)
		} else {
			fmt.Fprintf(&buf, "%s ", spinnerStyled)
		}
	} else if textToDraw != "" {
		fmt.Fprint(&buf, textToDraw)
	}

	// Restore cursor
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func DrawStaticPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	DrawStaticPromptSeparatorWithSpinner(w, showThinking, reasoningEffort, theme, "")
}

func DrawStaticPromptSeparatorWithSpinner(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	height := getWriterHeight(w)
	if height <= 0 {
		_, h := getTerminalSize()
		height = h
	}

	var buf bytes.Buffer
	// Save cursor, move cursor to height-3-pasteLinesOffset absolutely, clear line, print separator, restore cursor
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", height-3-pasteLinesOffset)
	PrintPromptSeparatorWithSpinner(&buf, showThinking, reasoningEffort, theme, spinnerFrame)
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func PrintSessionHistory(w io.Writer, messages []db.Message, theme UITheme, cfg *config.Config) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)

	var lastRole string
	for _, msg := range messages {
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "user" {
			if strings.HasPrefix(msg.Content, "[user manually executed local shell command: `") {
				firstTick := strings.Index(msg.Content, "`")
				if firstTick != -1 {
					rest := msg.Content[firstTick+1:]
					secondTick := strings.Index(rest, "`")
					if secondTick != -1 {
						cmdStr := rest[:secondTick]
						output := ""
						newLineIdx := strings.Index(rest, "\n")
						if newLineIdx != -1 {
							output = rest[newLineIdx+1:]
						}
						if lastRole != "" {
							fmt.Fprintln(w)
						}
						fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> !"), cmdStr)
						if output != "" {
							fmt.Fprint(w, output)
						}
						lastRole = "user"
						continue
					}
				}
			}

			if lastRole != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, borderStyle.Render("─── prompt ───────────────────────────────────────────────"))
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)
		} else if msg.Role == "assistant" {
			if lastRole == "tool" {
				fmt.Fprintln(w)
			}
			hasPrintedAnything := false
			if msg.ReasoningContent != "" && cfg.ShowThinking {
				dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintln(w, dimStyle.Render(strings.TrimRight(msg.ReasoningContent, "\r\n")))
				fmt.Fprintln(w)

				iconStyle := style.NewStyle().Foreground(theme.Success)
				labelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintf(w, "%s %s\n", iconStyle.Render("✔"), labelStyle.Render("thought"))
				hasPrintedAnything = true
			}

			if msg.Content != "" {
				if hasPrintedAnything {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, msg.Content)
				hasPrintedAnything = true
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if hasPrintedAnything {
						fmt.Fprintln(w)
					}
					var path string
					var argsMap map[string]interface{}
					if json.Unmarshal([]byte(tc.Function.Arguments), &argsMap) == nil {
						if p, ok := argsMap["path"].(string); ok {
							path = p
						} else if c, ok := argsMap["command"].(string); ok {
							path = c
						}
					}
					isPathTool := tc.Function.Name == "read" || tc.Function.Name == "write" || tc.Function.Name == "edit" || tc.Function.Name == "ls" || tc.Function.Name == "grep" || tc.Function.Name == "find" || tc.Function.Name == "bash"
					var symbol string
					if tc.Function.Name == "write" {
						symbol = style.NewStyle().Foreground(theme.Success).Bold(true).Render("◆")
					} else {
						symbol = style.NewStyle().Foreground(theme.Success).Bold(true).Render("▸")
					}
					var title string
					if isPathTool && path != "" {
						title = FormatToolTitle(symbol, tc.Function.Name, path, theme)
					} else {
						title = FormatToolTitle(symbol, tc.Function.Name, "", theme)
					}
					fmt.Fprintln(w, title)
					hasPrintedAnything = true
				}
			}
		} else if msg.Role == "tool" {
			var toolName string
			var argsJSON string
			for i := len(messages) - 1; i >= 0; i-- {
				m := messages[i]
				if m.Role == "assistant" {
					for _, tc := range m.ToolCalls {
						if tc.ID == msg.ToolCallID {
							toolName = tc.Function.Name
							argsJSON = tc.Function.Arguments
							break
						}
					}
				}
				if toolName != "" {
					break
				}
			}
			if toolName == "" {
				toolName = msg.Name
			}
			RenderToolOutput(w, msg.Content, false, cfg.CollapseResults, theme, toolName, argsJSON, -1)
		}
		lastRole = msg.Role
	}
}

func RenderProviders(w io.Writer, cfg *config.Config, theme UITheme) {
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("configured endpoint providers") + "\n\n")

	if cfg.Providers == nil || len(cfg.Providers) == 0 {
		sb.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no endpoint providers configured)") + "\n")
	} else {
		var keys []string
		for k := range cfg.Providers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			p := cfg.Providers[name]
			marker := "  "
			if name == cfg.ActiveProvider {
				marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
			}

			apiKeyDisplay := "none"
			if p.ApiKey != "" {
				apiKeyDisplay = "configured"
			}

			sb.WriteString(fmt.Sprintf("%s%-12s : URL: %s | Model: %s | API Key: %s\n",
				marker,
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
				p.Endpoint,
				p.Model,
				apiKeyDisplay,
			))
		}
	}

	activeMarker := "  "
	if cfg.ActiveProvider == "" {
		activeMarker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
	}
	sb.WriteString(fmt.Sprintf("\n%s%-12s : URL: %s | Model: %s | (default settings)\n",
		activeMarker,
		style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("default"),
		cfg.Endpoint,
		cfg.Model,
	))

	sb.WriteString("\ntip: manage providers via REPL: /provider add/select/model/remove/list")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}

