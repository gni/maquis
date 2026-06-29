package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

func HandleMCPCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme UITheme,
	w io.Writer,
	rlInput io.Reader,
) {
	calcHistoryTokens := func() (int, int) {
		return a.GetGlobalTokens(*messages, nil)
	}

	if len(parts) < 2 {
		var input io.Reader = rlInput
		if input == nil {
			input = os.Stdin
			if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
				defer tty.Close()
				input = tty
			}
		}
		var output io.Writer = os.Stdout
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			output = tty
		}

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveMCPConfig(a.Config, theme, input, output, a.ConfigPath)
		InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			a.Config = newConfig
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}

		// Clear screen and redraw everything up to history
		fmt.Fprint(w, "\x1b[H\x1b[2J")
		if len(a.McpStartErrors) > 0 {
			RenderMCPStartupErrors(w, a.McpStartErrors, theme)
		}
		PrintBanner(w, a)

		if errInteractive != nil {
			fmt.Fprintln(w, "interactive mcp config cancelled.")
		} else if newConfig != nil {
			fmt.Fprintf(w, "mcp configuration updated and saved to %s\n", a.ConfigPath)
			// Restart MCP servers with the new config
			a.StopMCPServers()
			if len(a.Config.MCPServers) > 0 {
				_ = a.StartMCPServers(a.Config.MCPServers)
			}
		}

		PrintSessionHistory(w, *messages, theme, a.Config)
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
		return
	}

	sub := parts[1]
	switch sub {
	case "list":
		RenderMCPServers(w, a.Config, theme)
		
		// Show connection status
		mcpStatuses := a.GetMCPServersStatus()
		if len(a.Config.MCPServers) > 0 {
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("\nmcp connection status:"))
			var keys []string
			for k := range a.Config.MCPServers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, name := range keys {
				status, active := mcpStatuses[name]
				cfg := a.Config.MCPServers[name]
				
				if cfg.Disabled {
					status = "\x1b[31mdisabled\x1b[0m"
				} else if !active {
					if err, failed := a.McpStartErrors[name]; failed {
						status = fmt.Sprintf("failed to start (%v)", err)
					} else {
						status = "not connected"
					}
				}
				fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
			}
		}
	case "add":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /mcp add <name> <url> [headerKey:headerVal]...")
			return
		}
		name := parts[2]
		url := parts[3]
		headers := make(map[string]string)
		for _, arg := range parts[4:] {
			hParts := strings.SplitN(arg, ":", 2)
			if len(hParts) != 2 {
				hParts = strings.SplitN(arg, "=", 2)
			}
			if len(hParts) == 2 {
				headers[strings.TrimSpace(hParts[0])] = strings.TrimSpace(hParts[1])
			}
		}

		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		a.Config.MCPServers[name] = config.MCPServerConfig{
			URL:     url,
			Headers: headers,
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' successfully added/updated.\n", name)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}
		
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	case "enable", "disable":
		if len(parts) < 3 {
			fmt.Fprintf(w, "usage: /mcp %s <name>\n", sub)
			return
		}
		name := parts[2]
		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		cfg, ok := a.Config.MCPServers[name]
		if !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}
		
		cfg.Disabled = (sub == "disable")
		a.Config.MCPServers[name] = cfg
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' successfully %sd.\n", name, sub)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}

		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /mcp remove <name>")
			return
		}
		name := parts[2]
		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		if _, ok := a.Config.MCPServers[name]; !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}

		delete(a.Config.MCPServers, name)
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' removed.\n", name)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}

		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	case "tools":
		mcpTools := a.GetMCPTools()
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("available mcp tools:"))
		if len(mcpTools) == 0 {
			fmt.Fprintln(w, "  (no tools registered)")
		} else {
			for _, t := range mcpTools {
				fmt.Fprintf(w, "  - %s: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(t.Function.Name),
					t.Function.Description,
				)
			}
		}
	default:
		fmt.Fprintf(w, "unknown subcommand '%s'.\n", sub)
		printMCPHelp(w, theme)
	}
}

func printMCPHelp(w io.Writer, theme UITheme) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Usage:"))
	fmt.Fprintln(w, "  /mcp                                       Open the interactive MCP editor")
	fmt.Fprintln(w, "  /mcp list                                  List all configured MCP servers and status")
	fmt.Fprintln(w, "  /mcp tools                                 List all registered MCP tools")
	fmt.Fprintln(w, "  /mcp add <name> <url> [headerKey:val]...   Add or update an MCP server configuration")
	fmt.Fprintln(w, "  /mcp enable <name>                         Enable an MCP server")
	fmt.Fprintln(w, "  /mcp disable <name>                        Disable an MCP server")
	fmt.Fprintln(w, "  /mcp remove <name>                         Remove an MCP server configuration")
}

func RunInteractiveMCPConfig(
	cfg *config.Config,
	theme UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
	configPath string,
) (*config.Config, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return cfg, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, oldState)

	fmt.Fprint(rlOutput, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(rlOutput, "\x1b[?25h\x1b[?1049l")

	cloned := *cfg
	if cloned.MCPServers == nil {
		cloned.MCPServers = make(map[string]config.MCPServerConfig)
	}

	searchQuery := ""
	selectedIdx := 0
	selectedServer := ""

	// Helper to get first key alphabetically
	getFirstServer := func() string {
		if len(cloned.MCPServers) == 0 {
			return ""
		}
		var keys []string
		for k := range cloned.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys[0]
	}

	selectedServer = getFirstServer()

	for {
		// Ensure selectedServer is valid if servers exist
		if selectedServer != "" {
			if _, exists := cloned.MCPServers[selectedServer]; !exists {
				selectedServer = getFirstServer()
			}
		} else if len(cloned.MCPServers) > 0 {
			selectedServer = getFirstServer()
		}

		type settingItem struct {
			id          string
			name        string
			value       func() string
			description string
			onToggle    func()
			onEdit      func(newVal string) error
		}

		var items []*settingItem
		items = []*settingItem{
			{
				id:          "selected_server",
				name:        "selected mcp server",
				value:       func() string {
					if selectedServer == "" {
						return "none"
					}
					return selectedServer
				},
				description: "Currently selected MCP server configuration to view/edit",
				onToggle: func() {
					if len(cloned.MCPServers) == 0 {
						return
					}
					var keys []string
					for k := range cloned.MCPServers {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					idx := -1
					for i, k := range keys {
						if selectedServer == k {
							idx = i
							break
						}
					}
					nextIdx := (idx + 1) % len(keys)
					selectedServer = keys[nextIdx]
				},
			},
		}

		if selectedServer != "" {
			srvName := selectedServer
			srv := cloned.MCPServers[srvName]

			items = append(items, &settingItem{
				id:          "server_url",
				name:        "server url",
				value:       func() string { return cloned.MCPServers[srvName].URL },
				description: fmt.Sprintf("SSE endpoint URL for MCP server '%s'", srvName),
				onEdit: func(newVal string) error {
					if newVal == "" {
						return fmt.Errorf("URL cannot be empty")
					}
					curr := cloned.MCPServers[srvName]
					curr.URL = newVal
					cloned.MCPServers[srvName] = curr
					return nil
				},
			})

			items = append(items, &settingItem{
				id:          "server_enabled",
				name:        "status",
				value:       func() string { if cloned.MCPServers[srvName].Disabled { return "\x1b[31mdisabled\x1b[0m" } else { return "\x1b[32menabled\x1b[0m" } },
				description: fmt.Sprintf("Toggle to enable or disable MCP server '%s'", srvName),
				onToggle: func() {
					curr := cloned.MCPServers[srvName]
					curr.Disabled = !curr.Disabled
					cloned.MCPServers[srvName] = curr
				},
			})

			// Add setting items for each existing header
			var headerKeys []string
			for hk := range srv.Headers {
				headerKeys = append(headerKeys, hk)
			}
			sort.Strings(headerKeys)

			for _, hk := range headerKeys {
				headerKey := hk
				items = append(items, &settingItem{
					id:          "header_" + headerKey,
					name:        fmt.Sprintf("  header: %s", headerKey),
					value:       func() string { return cloned.MCPServers[srvName].Headers[headerKey] },
					description: fmt.Sprintf("Custom header value for '%s' sent to '%s'", headerKey, srvName),
					onEdit: func(newVal string) error {
						curr := cloned.MCPServers[srvName]
						if curr.Headers == nil {
							curr.Headers = make(map[string]string)
						}
						if newVal == "" {
							delete(curr.Headers, headerKey)
						} else {
							curr.Headers[headerKey] = newVal
						}
						cloned.MCPServers[srvName] = curr
						return nil
					},
				})
			}

			// Actions for the selected server
			items = append(items, &settingItem{
				id:          "action_add_header",
				name:        "[ add custom header ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Add a new custom header to send to MCP server '%s'", srvName),
				onToggle: func() {
					fmt.Fprintf(rlOutput, "\r\n\r\n  === Add Header to '%s' ===\r\n", srvName)
					fmt.Fprint(rlOutput, "  Enter header name (e.g. Authorization, X-Api-Key): ")
					hName, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					hName = strings.TrimSpace(hName)
					if hName != "" {
						fmt.Fprintf(rlOutput, "  Enter value for '%s': ", hName)
						hVal, err := readInputRaw(rlInput, rlOutput)
						if err != nil {
							return
						}
						hVal = strings.TrimSpace(hVal)

						curr := cloned.MCPServers[srvName]
						if curr.Headers == nil {
							curr.Headers = make(map[string]string)
						}
						curr.Headers[hName] = hVal
						cloned.MCPServers[srvName] = curr
					}
				},
			})
		}

		// Global MCP Actions
		items = append(items, &settingItem{
			id:          "action_add_server",
			name:        "[ add new mcp server ]",
			value:       func() string { return "" },
			description: "Add a new MCP server configuration by entering name and URL",
			onToggle: func() {
				fmt.Fprint(rlOutput, "\r\n\r\n  === Add New MCP Server ===\r\n")
				fmt.Fprint(rlOutput, "  Enter server name: ")
				sName, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				sName = strings.TrimSpace(sName)

				if sName != "" {
					fmt.Fprint(rlOutput, "  Enter SSE endpoint URL: ")
					sURL, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					sURL = strings.TrimSpace(sURL)

					if sURL != "" {
						if cloned.MCPServers == nil {
							cloned.MCPServers = make(map[string]config.MCPServerConfig)
						}
						cloned.MCPServers[sName] = config.MCPServerConfig{
							URL:     sURL,
							Headers: make(map[string]string),
						}
						selectedServer = sName
					}
				}
			},
		})

		if selectedServer != "" {
			srvName := selectedServer
			items = append(items, &settingItem{
				id:          "action_remove_server",
				name:        "[ remove selected server ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Delete the MCP server configuration for '%s'", srvName),
				onToggle: func() {
					delete(cloned.MCPServers, srvName)
					selectedServer = getFirstServer()
				},
			})
		}

		// Filter items based on search query
		var filtered []*settingItem
		for _, item := range items {
			valStr := item.value()
			match := searchQuery == "" ||
				strings.Contains(strings.ToLower(item.name), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(valStr), strings.ToLower(searchQuery)) ||
				strings.Contains(strings.ToLower(item.description), strings.ToLower(searchQuery))
			if match {
				filtered = append(filtered, item)
			}
		}

		if selectedIdx >= len(filtered) {
			selectedIdx = len(filtered) - 1
		}
		if selectedIdx < 0 {
			selectedIdx = 0
		}

		var buf strings.Builder
		buf.WriteString("\x1b[H")

		titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		buf.WriteString(titleStyle.Render("mcp servers settings setup"))
		buf.WriteString("\n\n")

		searchLabelStyle := style.NewStyle().Foreground(theme.Text)
		buf.WriteString(searchLabelStyle.Render("  search:  "))

		searchValStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		buf.WriteString(searchValStyle.Render(searchQuery))
		buf.WriteString("\n")

		underlineStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(underlineStyle.Render("           ────────────────────"))
		buf.WriteString("\n\n")

		if len(filtered) == 0 {
			dimStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
			buf.WriteString(dimStyle.Render("  (no matching settings found)"))
			buf.WriteString("\n")
		} else {
			for idx, item := range filtered {
				nameStr := item.name
				valStr := item.value()

				keyColWidth := 28
				nameLen := len(nameStr)
				leader := ""
				if nameLen < keyColWidth {
					leader = strings.Repeat("·", keyColWidth-nameLen)
				}

				if idx == selectedIdx {
					markerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					nameStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
					bracketStyle := style.NewStyle().Foreground(theme.Secondary)

					valStrFormatted := ""
					if valStr != "" {
						valStrFormatted = fmt.Sprintf("%s %s %s", bracketStyle.Render("["), valStyle.Render(valStr), bracketStyle.Render("]"))
					}
					buf.WriteString(fmt.Sprintf("%s  %s %s %s\n", markerStyle.Render("▸"), nameStyle.Render(nameStr), leaderStyle.Render(leader), valStrFormatted))
				} else {
					nameStyle := style.NewStyle().Foreground(theme.Text)
					leaderStyle := style.NewStyle().Foreground(theme.Border)
					valStyle := style.NewStyle().Foreground(theme.Secondary)
					buf.WriteString(fmt.Sprintf("   %s %s %s\n", nameStyle.Render(nameStr), leaderStyle.Render(leader), valStyle.Render(valStr)))
				}
			}
		}

		buf.WriteString("\n")

		if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
			descStyle := style.NewStyle().Foreground(theme.Success)
			buf.WriteString(fmt.Sprintf("  %s\n", descStyle.Render(filtered[selectedIdx].description)))
		} else {
			buf.WriteString("\n")
		}

		buf.WriteString("\n")
		navStyle := style.NewStyle().Foreground(theme.Border)
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("↑/↓ navigate · enter select/edit · esc clear search/exit")))
		buf.WriteString(fmt.Sprintf("  %s\n", navStyle.Render("esc to save and exit")))

		buf.WriteString("\x1b[J")

		outputStr := strings.ReplaceAll(buf.String(), "\n", "\x1b[K\r\n")
		_, _ = rlOutput.Write([]byte(outputStr))

		var readBuf [16]byte
		n, err := rlInput.Read(readBuf[:])
		if err != nil {
			return nil, err
		}

		if n == 1 {
			char := readBuf[0]

			if char == 3 || char == 4 {
				return nil, fmt.Errorf("cancelled")
			}

			if char == 13 || char == 10 {
				if len(filtered) > 0 && selectedIdx >= 0 && selectedIdx < len(filtered) {
					item := filtered[selectedIdx]
					if item.onToggle != nil {
						item.onToggle()
					} else if item.onEdit != nil {
						fmt.Fprintf(rlOutput, "\r\n\r\n  edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  enter new value (empty to delete if header): ")

						newVal, err := readInputRaw(rlInput, rlOutput)
						if err == nil {
							newVal = strings.TrimSpace(newVal)
							err = item.onEdit(newVal)
							if err != nil {
								fmt.Fprintf(rlOutput, "\r\n  error: %v. press enter to continue...", err)
								_, _ = readInputRaw(rlInput, rlOutput)
							}
						}
					}
				}
				continue
			}

			if char == 27 {
				if searchQuery != "" {
					searchQuery = ""
				} else {
					return &cloned, nil
				}
				continue
			}

			if char == 127 || char == 8 {
				if len(searchQuery) > 0 {
					searchQuery = searchQuery[:len(searchQuery)-1]
				}
				continue
			}

			if char >= 32 && char <= 126 {
				searchQuery += string(char)
				continue
			}
		}

		if n >= 3 && readBuf[0] == 27 && readBuf[1] == '[' {
			switch readBuf[2] {
			case 'A':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx - 1 + len(filtered)) % len(filtered)
				}
			case 'B':
				if len(filtered) > 0 {
					selectedIdx = (selectedIdx + 1) % len(filtered)
				}
			}
		}
	}
}
