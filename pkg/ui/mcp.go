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
	kiReader *keyInterceptorReader,
) {
	calcHistoryTokens := func() (int, int, bool) {
		var mam *agent.MultiAgentManager
		if kiReader != nil {
			mam = kiReader.mam
		}
		return calculateActiveTokenUsage(a, *messages, activeToolAllowlist(kiReader), mam)
	}

	if len(parts) < 2 {
		var input io.Reader
		if kiReader != nil {
			input = kiReader
		} else {
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
			a.ApplyConfig(newConfig)
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}

		if errInteractive == nil && newConfig != nil {
			// Restart MCP servers with the new config
			a.StopMCPServers()
			if len(a.Config.MCPServers) > 0 {
				_ = a.StartMCPServers(a.Config.MCPServers)
			}
		}

		if kiReader != nil {
			redrawScreen(w, a, kiReader, kiReader.rl)
		} else {
			redrawScreen(w, a, nil, nil)
		}
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

		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
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

		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
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

		pTok, cTok, estimated := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
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

	itemsProvider := func() []*settingItem {
		// Ensure selectedServer is valid if servers exist
		if selectedServer != "" {
			if _, exists := cloned.MCPServers[selectedServer]; !exists {
				selectedServer = getFirstServer()
			}
		} else if len(cloned.MCPServers) > 0 {
			selectedServer = getFirstServer()
		}

		var items []*settingItem
		items = []*settingItem{
			{
				id:   "selected_server",
				name: "selected mcp server",
				value: func() string {
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
			cfgServer := cloned.MCPServers[srvName]

			items = append(items, &settingItem{
				id:          "server_url",
				name:        "server url",
				value:       func() string { return cloned.MCPServers[srvName].URL },
				description: "The HTTP/SSE URL of the MCP server",
				onEdit: func(newVal string) error {
					if newVal == "" {
						return fmt.Errorf("url cannot be empty")
					}
					curr := cloned.MCPServers[srvName]
					curr.URL = newVal
					cloned.MCPServers[srvName] = curr
					return nil
				},
			})

			items = append(items, &settingItem{
				id:   "server_enabled",
				name: "status",
				value: func() string {
					if cloned.MCPServers[srvName].Disabled {
						return "\x1b[31mdisabled\x1b[0m"
					} else {
						return "\x1b[32menabled\x1b[0m"
					}
				},
				description: fmt.Sprintf("Toggle to enable or disable MCP server '%s'", srvName),
				onToggle: func() {
					curr := cloned.MCPServers[srvName]
					curr.Disabled = !curr.Disabled
					cloned.MCPServers[srvName] = curr
				},
			})

			// Extract and sort headers
			var hKeys []string
			for h := range cfgServer.Headers {
				hKeys = append(hKeys, h)
			}
			sort.Strings(hKeys)

			for _, hk := range hKeys {
				headerName := hk
				items = append(items, &settingItem{
					id:          "header_" + headerName,
					name:        "  header: " + headerName,
					value:       func() string { return cloned.MCPServers[srvName].Headers[headerName] },
					description: fmt.Sprintf("HTTP Header '%s' value for '%s'", headerName, srvName),
					onEdit: func(newVal string) error {
						curr := cloned.MCPServers[srvName]
						if newVal == "" {
							delete(curr.Headers, headerName)
						} else {
							curr.Headers[headerName] = newVal
						}
						cloned.MCPServers[srvName] = curr
						return nil
					},
				})
			}

			items = append(items, &settingItem{
				id:          "action_add_header",
				name:        "  [ add HTTP header ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Add a custom HTTP header for '%s'", srvName),
				onToggle: func() {
					fmt.Fprint(rlOutput, "\r\n\r\n  === Add Header ===\r\n")
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

		return items
	}

	err = runSettingsMenuLoop(rlInput, rlOutput, theme, "mcp servers settings setup", itemsProvider, nil)
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}
