package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"maquis/pkg/ui/style"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"github.com/alecthomas/chroma/v2/quick"
)

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
		{"/set <key> <value>", "modify settings dynamically (alias)"},
		{"/session <list/new/load/clear>", "manage persistent chat sessions interactively"},
		{"/skills", "list all available reference skills"},
		{"/skills load <name>", "explicitly load a reference skill into the context"},
		{"/mcp <enable/disable>", "list and toggle mcp servers and active tool schemas"},
		{"/provider", "manage ai endpoints, keys, and model profiles"},
		{"/plugins", "list all registered custom plugin tools"},
		{"/extensions", "list all custom slash command extensions"},
		{"/reload", "re-scan and hot-reload custom plugins and tools"},
		{"/agent <list/join/spawn/skill/remove>", "manage multi-agent swarm threads interactively"},
		{"/task <list/view/stream/remove>", "manage async background tasks"},
		{"/clear", "clear conversation and start a new one"},
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
	termW, _ := getTerminalSize()
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

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
			"  %-20s %.2f\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %d tokens\n"+
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
		keyStyle.Render("active provider:"), valStyle.Render(cfg.ActiveProvider),
		keyStyle.Render("temperature:"), cfg.Temperature,
		keyStyle.Render("auto-approve:"), approveVal,
		keyStyle.Render("show thinking:"), cfg.ShowThinking,
		keyStyle.Render("collapse results:"), cfg.CollapseResults,
		keyStyle.Render("show tokens:"), cfg.ShowTokens,
		keyStyle.Render("context limit:"), cfg.ContextWindowLimit,
		keyStyle.Render("max completion tokens:"), cfg.MaxCompletionTokens,
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

	var simpleLines []string
	var blockLines []string

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

			var blockSb strings.Builder
			blockSb.WriteString(fmt.Sprintf("%s:\n", keyStyle.Render(k)))
			lines := strings.Split(highlightedStr, "\n")
			for i, line := range lines {
				if line == "" && i == len(lines)-1 {
					continue
				}
				blockSb.WriteString(fmt.Sprintf("%s\n", line))
			}
			blockLines = append(blockLines, strings.TrimSuffix(blockSb.String(), "\n"))

		case "edits", "updates":
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


func extractToolTarget(toolName string, argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var argsMap map[string]interface{}
	_ = json.Unmarshal([]byte(argsJSON), &argsMap)

	getString := func(key string) string {
		if argsMap != nil {
			if val, ok := argsMap[key]; ok {
				if s, ok := val.(string); ok {
					return s
				}
			}
		}
		// Fallback for incomplete JSON
		regex := regexp.MustCompile(fmt.Sprintf(`"%s"\s*:\s*"([^"]+)"`, regexp.QuoteMeta(key)))
		matches := regex.FindStringSubmatch(argsJSON)
		if len(matches) > 1 {
			return matches[1]
		}
		return ""
	}

	// 1. Command execution tools (bash, run_command, exec, etc.)
	if strings.Contains(toolName, "bash") || strings.Contains(toolName, "command") || strings.Contains(toolName, "run") || strings.Contains(toolName, "exec") {
		if c := getString("CommandLine"); c != "" {
			return c
		}
		if c := getString("command"); c != "" {
			return c
		}
	}

	// 2. Search / query tools (grep, grep_search, search_web, find_files, etc.)
	if strings.Contains(toolName, "grep") || strings.Contains(toolName, "search") || strings.Contains(toolName, "find") || strings.Contains(toolName, "query") {
		q := getString("pattern")
		if q == "" {
			q = getString("Query")
		}
		if q == "" {
			q = getString("query")
		}

		p := getString("SearchPath")
		if p == "" {
			p = getString("path")
		}
		if p == "" {
			p = getString("DirectoryPath")
		}
		if p == "" {
			p = getString("dirPath")
		}

		if q != "" && p != "" {
			wd, err := os.Getwd()
			if err == nil {
				if rel, err := filepath.Rel(wd, p); err == nil {
					p = rel
				}
			}
			return fmt.Sprintf("%s (in %s)", q, p)
		}
		if q != "" {
			return q
		}
		if p != "" {
			return p
		}
	}

	// 3. File / Directory tools
	if strings.Contains(toolName, "file") || strings.Contains(toolName, "dir") || strings.Contains(toolName, "read") || strings.Contains(toolName, "write") || strings.Contains(toolName, "edit") || strings.Contains(toolName, "replace") || toolName == "ls" || toolName == "view" {
		if p := getString("AbsolutePath"); p != "" {
			return p
		}
		if p := getString("TargetFile"); p != "" {
			return p
		}
		if p := getString("SearchPath"); p != "" {
			return p
		}
		if p := getString("DirectoryPath"); p != "" {
			return p
		}
		if p := getString("path"); p != "" {
			return p
		}
		if p := getString("target"); p != "" {
			return p
		}
		if p := getString("Target"); p != "" {
			return p
		}
	}

	// 4. Subagents / Spawning / Prompts
	if strings.Contains(toolName, "subagent") || strings.Contains(toolName, "spawn") || strings.Contains(toolName, "task") || strings.Contains(toolName, "ask") || strings.Contains(toolName, "permission") {
		if p := getString("prompt"); p != "" {
			return p
		}
		if p := getString("Prompt"); p != "" {
			return p
		}
		if p := getString("name"); p != "" {
			return p
		}
		if p := getString("id"); p != "" {
			return p
		}
	}

	// 5. Fallback - check all keys in priority order
	keys := []string{
		"CommandLine", "command", "query", "Query", "pattern", "prompt", "Prompt",
		"AbsolutePath", "TargetFile", "SearchPath", "DirectoryPath", "path", "target", "Target", "name", "id",
	}
	for _, key := range keys {
		if val := getString(key); val != "" {
			return val
		}
	}

	return ""
}

func RenderToolHeader(w io.Writer, theme UITheme, toolName string, argsJSON string) {
	var symbol string
	if toolName == "write" || strings.Contains(toolName, "write") || strings.Contains(toolName, "replace") {
		symbol = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◆")
	} else {
		symbol = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("▸")
	}

	pathVal := extractToolTarget(toolName, argsJSON)

	title := FormatToolTitle(symbol, toolName, pathVal, theme)
	fmt.Fprintln(w, title)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme UITheme, toolName string, argsJSON string, newlineCount int) {
	var finalDot string
	if toolName == "write" || strings.Contains(toolName, "write") || strings.Contains(toolName, "replace") {
		if !isError {
			finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("◆")
		} else {
			finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("◆")
		}
	} else {
		if !isError {
			finalDot = style.NewStyle().Foreground(theme.Success).Bold(true).Render("✔")
		} else {
			finalDot = style.NewStyle().Foreground(theme.Error).Bold(true).Render("✖")
		}
	}

	pathVal := extractToolTarget(toolName, argsJSON)

	finalTitle := FormatToolTitle(finalDot, toolName, pathVal, theme)

	_, termH := getTerminalSize()
	if termH <= 0 {
		termH = 24
	}
	if newlineCount > termH-6 {
		newlineCount = -2
	}

	var pp *PromptPreservingWriter
	curr := w
	for {
		if p, ok := curr.(*PromptPreservingWriter); ok {
			pp = p
			break
		}
		if unwrapper, ok := curr.(interface{ Unwrap() io.Writer }); ok {
			curr = unwrapper.Unwrap()
		} else {
			break
		}
	}

	if pp != nil {
		if newlineCount == -2 {
			fmt.Fprintln(w, finalTitle)
		} else if newlineCount >= 0 {
			if newlineCount > 0 {
				// Move cursor to the header line on the terminal, clear and overwrite
				fmt.Fprintf(pp.inner, "\x1b[%d;1H\x1b[2K", pp.printLine-newlineCount)
				fmt.Fprint(pp.inner, finalTitle)

				// Clear intermediate argument lines
				for i := 1; i <= newlineCount; i++ {
					fmt.Fprintf(pp.inner, "\x1b[%d;1H\x1b[2K", pp.printLine-newlineCount+i)
				}

				// Update pp's tracked position to the line right below the header
				pp.SetPrintLine(pp.printLine - newlineCount + 1)
				pp.SetPrintCol(1)
			} else {
				fmt.Fprintf(pp.inner, "\x1b[%d;1H\x1b[2K", pp.printLine)
				fmt.Fprint(pp.inner, finalTitle)
				pp.SetPrintCol(len(stripAnsi(finalTitle)) + 1)
			}
		}
	} else {
		if newlineCount == -2 {
			fmt.Fprintln(w, finalTitle)
		} else if newlineCount >= 0 {
			if newlineCount > 0 {
				// Move cursor up to the header line
				fmt.Fprintf(w, "\x1b[%dA\r", newlineCount)
			} else {
				fmt.Fprint(w, "\r")
			}
			// Clear line and print final header
			fmt.Fprint(w, "\x1b[2K")
			fmt.Fprint(w, finalTitle)

			if newlineCount > 0 {
				// Move down and clear all the intermediate streamed argument/parameter lines
				for i := 1; i <= newlineCount; i++ {
					fmt.Fprint(w, "\n\x1b[2K")
				}
				// Move cursor back up to position it directly below the completed header
				if newlineCount > 1 {
					fmt.Fprintf(w, "\x1b[%dA\r", newlineCount-1)
				} else {
					fmt.Fprint(w, "\r")
				}
			} else {
				fmt.Fprint(w, "\r\n")
			}
		}
	}

	fmt.Fprintln(w)

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

	isCodeTool := toolName == "read" || toolName == "write" ||
		strings.Contains(toolName, "read") ||
		strings.Contains(toolName, "write") ||
		strings.Contains(toolName, "view") ||
		strings.Contains(toolName, "content")

	if !isError && (isCodeTool || isJSON) {
		lang := "plaintext"
		if isJSON {
			lang = "json"
		} else {
			var args struct {
				Path               string `json:"path"`
				AbsolutePath       string `json:"AbsolutePath"`
				TargetFile         string `json:"TargetFile"`
				Content            string `json:"content"`
				WriteContent       string `json:"write_content"`
				CodeContent        string `json:"CodeContent"`
				ReplacementContent string `json:"ReplacementContent"`
			}
			if argsJSON != "" {
				err := json.Unmarshal([]byte(argsJSON), &args)
				filePath := args.Path
				if filePath == "" {
					filePath = args.AbsolutePath
				}
				if filePath == "" {
					filePath = args.TargetFile
				}
				if filePath != "" {
					ext := filepath.Ext(filePath)
					if len(ext) > 1 {
						lang = ext[1:]
					}
				}

				if strings.Contains(toolName, "write") || strings.Contains(toolName, "replace") {
					if err != nil {
						body = argsJSON
						lang = "json"
					} else {
						if args.CodeContent != "" {
							body = args.CodeContent
						} else if args.ReplacementContent != "" {
							body = args.ReplacementContent
						} else if args.Content != "" {
							body = args.Content
						} else if args.WriteContent != "" {
							body = args.WriteContent
						}
					}
				}
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

	if collapse && len(lines) > 0 {
		collapsedCount := len(lines)
		collapsedMsg := fmt.Sprintf("  ... [%d lines collapsed. Press Ctrl+O or type /expand to view output] ...", collapsedCount)
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(collapsedMsg))
	} else {
		for _, line := range lines {
			printLine(line)
		}
	}
	fmt.Fprintln(w)
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

	termW, _ := getTerminalSize()

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

	var innerTitle string
	if path != "" {
		relPath := path
		isNonFilePathTool := toolName == "spawn_subagent" || strings.HasPrefix(toolName, "subagent__") || toolName == "task_status" || toolName == "task_kill" || toolName == "load_skill"
		if toolName != "bash" && !isNonFilePathTool {
			wd, err := os.Getwd()
			if err == nil {
				if rel, err := filepath.Rel(wd, path); err == nil {
					relPath = rel
				}
			}
		}
		if (toolName == "spawn_subagent" || strings.HasPrefix(toolName, "subagent__")) && len(relPath) > 60 {
			relPath = relPath[:57] + "..."
		}
		innerTitle = fmt.Sprintf("%s %s %s", symbol, toolStyle.Render(toolName), pathStyle.Render(relPath))
	} else {
		innerTitle = fmt.Sprintf("%s %s", symbol, toolStyle.Render(toolName))
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	width, _ := getTerminalSize()
	if width <= 0 {
		width = 80
	}
	targetWidth := width - 2
	innerLen := utf8.RuneCountInString(stripAnsi(innerTitle))
	dashesCount := targetWidth - 5 - innerLen
	if dashesCount < 3 {
		dashesCount = 3
	}
	dashes := strings.Repeat("─", dashesCount)
	return fmt.Sprintf("%s%s %s", borderStyle.Render("─── "), innerTitle, borderStyle.Render(dashes))
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
	prefixLen := 11

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
	DrawStaticStatsLineLocked(w, theme, spinnerFrame, statsText)
}

func DrawStaticStatsLineLocked(w io.Writer, theme UITheme, spinnerFrame string, statsText string) {
	height := getWriterHeight(w)
	if height <= 0 {
		_, h := getTerminalSize()
		height = h
	}

	getUI().StateMu.Lock()
	if spinnerFrame == "" {
		if statsText != "" {
			getUI().LastStatsText = statsText
		}
	}
	textToDraw := statsText
	if textToDraw == "" && spinnerFrame == "" {
		textToDraw = getUI().LastStatsText
	}
	getUI().StateMu.Unlock()

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", height-4-getUI().PasteLinesOffset)

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

	fmt.Fprint(&buf, "\x1b8")
	_, _ = w.Write(buf.Bytes())
}

func DrawStaticPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStaticPromptSeparatorLocked(w, showThinking, reasoningEffort, theme)
}

func DrawStaticPromptSeparatorLocked(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	DrawStaticPromptSeparatorWithSpinnerLocked(w, showThinking, reasoningEffort, theme, "")
}

func DrawStaticPromptSeparatorWithSpinner(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStaticPromptSeparatorWithSpinnerLocked(w, showThinking, reasoningEffort, theme, spinnerFrame)
}

func DrawStaticPromptSeparatorWithSpinnerLocked(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	height := getWriterHeight(w)
	if height <= 0 {
		_, h := getTerminalSize()
		height = h
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", height-3-getUI().PasteLinesOffset)
	PrintPromptSeparatorWithSpinner(&buf, showThinking, reasoningEffort, theme, spinnerFrame)
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func renderMarkdownContent(w io.Writer, content string, theme UITheme) {
	lines := strings.Split(content, "\n")
	sr := &StreamRenderer{
		theme: theme,
		w:     w,
	}
	for i, line := range lines {
		if sr.inCodeBlock {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				sr.inCodeBlock = false
			} else {
				fmt.Fprint(w, style.NewStyle().Foreground(theme.Highlight).Render(line))
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		} else {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				sr.inCodeBlock = true
			} else {
				sr.printNormalLine(line)
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		}
	}
}

func PrintSessionHistory(w io.Writer, messages []db.Message, theme UITheme, cfg *config.Config) {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)

	var lastRole string
	for i, msg := range messages {
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "user" {
			if strings.HasPrefix(msg.Content, "[user manually executed slash command: `") {
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
						if lastRole != "" && lastRole != "tool" {
							fmt.Fprintln(w)
						}
						termW, _ := getTerminalSize()
						dashesCount := termW - 12
						if dashesCount < 5 {
							dashesCount = 5
						}
						separator := "─── prompt " + strings.Repeat("─", dashesCount)
						fmt.Fprintln(w, borderStyle.Render(separator))
						fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), cmdStr)
						if output != "" {
							fmt.Fprint(w, output)
						}
						lastRole = "user"
						continue
					}
				}
			}

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
			termW, _ := getTerminalSize()
			dashesCount := termW - 12
			if dashesCount < 5 {
				dashesCount = 5
			}
			separator := "─── prompt " + strings.Repeat("─", dashesCount)
			fmt.Fprintln(w, borderStyle.Render(separator))
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)
		} else if msg.Role == "assistant" {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(w, divider)

			hasPrintedAnything := false
			if msg.ReasoningContent != "" && cfg.ShowThinking {
				dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintln(w, dimStyle.Render(strings.TrimRight(msg.ReasoningContent, "\r\n")))
				fmt.Fprintln(w)

				iconStyle := style.NewStyle().Foreground(theme.Success)
				labelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				if msg.ReasoningDuration > 0 {
					fmt.Fprintf(w, "%s %s\n", iconStyle.Render("✔"), labelStyle.Render(fmt.Sprintf("thought (%.1fs)", msg.ReasoningDuration)))
				} else {
					fmt.Fprintf(w, "%s %s\n", iconStyle.Render("✔"), labelStyle.Render("thought"))
				}
				hasPrintedAnything = true
			}

			if msg.Content != "" {
				if hasPrintedAnything {
					fmt.Fprintln(w)
				}
				renderMarkdownContent(w, msg.Content, theme)
				fmt.Fprintln(w)
				hasPrintedAnything = true
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					hasResponse := false
					for j := i + 1; j < len(messages); j++ {
						if messages[j].Role == "tool" && messages[j].ToolCallID == tc.ID {
							hasResponse = true
							break
						}
					}

					if !hasResponse {
						if hasPrintedAnything {
							fmt.Fprintln(w)
						}
						
						isWriteTool := tc.Function.Name == "write" || strings.Contains(tc.Function.Name, "write") || strings.Contains(tc.Function.Name, "replace")
						
						if len(tc.Function.Arguments) > 0 && isWriteTool {
							RenderToolOutput(w, "", false, false, theme, tc.Function.Name, tc.Function.Arguments, -2)
						} else {
							path := extractToolTarget(tc.Function.Name, tc.Function.Arguments)
							var symbol string
							if isWriteTool {
								symbol = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("◆")
							} else {
								symbol = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("▸")
							}
							title := FormatToolTitle(symbol, tc.Function.Name, path, theme)
							fmt.Fprintln(w, title)
						}
						
						cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
						fmt.Fprintln(w, cancelStyle.Render("  [Operation Cancelled]"))
						hasPrintedAnything = true
					}
				}
			}
		} else if msg.Role == "tool" {
			var toolName string
			var argsJSON string
			for j := i - 1; j >= 0; j-- {
				m := messages[j]
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
			
			isError := strings.HasPrefix(msg.Content, "Error:") || strings.HasPrefix(msg.Content, "error:") || strings.Contains(msg.Content, "command failed:")
			RenderToolOutput(w, msg.Content, isError, cfg.CollapseResults, theme, toolName, argsJSON, -2)
		} else if msg.Role == "error" {
			if lastRole != "" {
				fmt.Fprintln(w)
			}
			termW, _ := getTerminalSize()
			errStyle := style.NewStyle().
				Border(style.RoundedBorder()).
				BorderForeground(theme.Error).
				Padding(1, 2).
				Margin(0, 0).
				MaxWidth(termW)
			
			titleStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
			bodyStyle := style.NewStyle().Foreground(theme.Text)
			
			content := fmt.Sprintf("%s\n\n%s", titleStyle.Render("error during generation:"), bodyStyle.Render(msg.Content))
			fmt.Fprint(w, errStyle.Render(content))
			fmt.Fprintln(w)
		}
		lastRole = msg.Role
	}
}

func RenderProviders(w io.Writer, cfg *config.Config, theme UITheme) {
	termW, _ := getTerminalSize()
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

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

func RenderMCPServers(w io.Writer, cfg *config.Config, theme UITheme) {
	termW, _ := getTerminalSize()
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("configured mcp servers") + "\n\n")

	if cfg.MCPServers == nil || len(cfg.MCPServers) == 0 {
		sb.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no mcp servers configured)") + "\n")
	} else {
		var keys []string
		for k := range cfg.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			srv := cfg.MCPServers[name]
			var headerParts []string
			for hk, hv := range srv.Headers {
				headerParts = append(headerParts, fmt.Sprintf("%s: %s", hk, hv))
			}
			sort.Strings(headerParts)
			headersDisplay := "none"
			if len(headerParts) > 0 {
				headersDisplay = strings.Join(headerParts, ", ")
			}

			sb.WriteString(fmt.Sprintf("  %-12s : URL: %s | Headers: %s\n",
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
				srv.URL,
				headersDisplay,
			))
		}
	}

	sb.WriteString("\ntip: manage mcp servers via REPL: /mcp list/add/remove or interactive setup: /mcp")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}