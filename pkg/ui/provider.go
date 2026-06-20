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

func HandleProviderCommand(
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
		newConfig, errInteractive := RunInteractiveProviderConfig(a.Config, theme, input, output, a.ConfigPath)
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
			fmt.Fprintln(w, "interactive provider config cancelled.")
		} else if newConfig != nil {
			fmt.Fprintf(w, "provider configuration updated and saved to %s\n", a.ConfigPath)
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
		listProviders(w, a.Config, theme)
	case "add":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /provider add <name> <endpoint> [api_key] [model]")
			return
		}
		name := parts[2]
		endpoint := parts[3]
		apiKey := ""
		model := ""
		if len(parts) > 4 {
			apiKey = parts[4]
		}
		if len(parts) > 5 {
			model = strings.Join(parts[5:], " ")
		}
		if a.Config.Providers == nil {
			a.Config.Providers = make(map[string]config.ProviderConfig)
		}
		a.Config.Providers[name] = config.ProviderConfig{
			Name:     name,
			Endpoint: endpoint,
			ApiKey:   apiKey,
			Model:    model,
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "Provider '%s' added successfully. To use it, run: /provider select %s\n", name, name)
	case "select", "use":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider select <name>")
			return
		}
		name := parts[2]
		if name == "none" || name == "default" {
			a.Config.ActiveProvider = ""
			_ = config.SaveConfig(a.ConfigPath, a.Config)
			fmt.Fprintln(w, "Switched to default endpoint settings.")
			pTok, cTok := calcHistoryTokens()
			UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
			DrawStatusBar(os.Stderr, theme)
			return
		}
		if _, ok := a.Config.Providers[name]; !ok {
			fmt.Fprintf(w, "error: provider '%s' not found.\n", name)
			return
		}
		a.Config.ActiveProvider = name
		a.Config.SyncActiveProvider()
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "Switched active provider to '%s' (Endpoint: %s, Model: %s).\n", name, a.Config.Endpoint, a.Config.Model)
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	case "model":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider model <model>")
			return
		}
		model := strings.Join(parts[2:], " ")
		a.Config.Model = model
		a.Config.UpdateActiveProvider()
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		if a.Config.ActiveProvider != "" {
			fmt.Fprintf(w, "Updated model for provider '%s' to '%s'.\n", a.Config.ActiveProvider, model)
		} else {
			fmt.Fprintf(w, "Updated model to '%s'.\n", model)
		}
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /provider remove <name>")
			return
		}
		name := parts[2]
		if _, ok := a.Config.Providers[name]; !ok {
			fmt.Fprintf(w, "error: provider '%s' not found.\n", name)
			return
		}
		delete(a.Config.Providers, name)
		if a.Config.ActiveProvider == name {
			a.Config.ActiveProvider = ""
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "Provider '%s' removed.\n", name)
		pTok, cTok := calcHistoryTokens()
		UpdateStatus(a.Config.Model, pTok, cTok, 0, a.Config.ContextWindowLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens)
		DrawStatusBar(os.Stderr, theme)
	default:
		fmt.Fprintf(w, "unknown subcommand '%s'.\n", sub)
		printProviderHelp(w, theme)
	}
}

func listProviders(w io.Writer, cfg *config.Config, theme UITheme) {
	if cfg.Providers == nil || len(cfg.Providers) == 0 {
		fmt.Fprintln(w, "No custom endpoint providers configured.")
		return
	}

	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Configured Endpoint Providers:"))

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

		fmt.Fprintf(w, " %s %-12s : URL: %s | Model: %s | API Key: %s\n",
			marker,
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
			p.Endpoint,
			p.Model,
			apiKeyDisplay,
		)
	}
	if cfg.ActiveProvider == "" {
		fmt.Fprintf(w, " %s %-12s : URL: %s | Model: %s | (Currently active default settings)\n",
			style.NewStyle().Foreground(theme.Success).Render("➔ "),
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("default"),
			cfg.Endpoint,
			cfg.Model,
		)
	}
}

func printProviderHelp(w io.Writer, theme UITheme) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Usage:"))
	fmt.Fprintln(w, "  /provider list                             List all endpoint providers")
	fmt.Fprintln(w, "  /provider add <name> <url> [key] [model]  Add a new provider profile")
	fmt.Fprintln(w, "  /provider select <name>                    Select active provider (use 'default' to reset)")
	fmt.Fprintln(w, "  /provider model <model>                    Set model for currently active provider")
	fmt.Fprintln(w, "  /provider remove <name>                    Remove a provider profile")
}

func RunInteractiveProviderConfig(
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

	type settingItem struct {
		id          string
		name        string
		value       func() string
		description string
		onToggle    func()
		onEdit      func(newVal string) error
	}

	searchQuery := ""
	selectedIdx := 0

	for {
		var items []*settingItem
		items = []*settingItem{
			{
				id:          "active_provider",
				name:        "active provider",
				value:       func() string {
					if cloned.ActiveProvider == "" {
						return "none (default)"
					}
					return cloned.ActiveProvider
				},
				description: "Currently active endpoint provider profile",
				onToggle: func() {
					var keys []string
					keys = append(keys, "")
					for k := range cloned.Providers {
						keys = append(keys, k)
					}
					sort.Strings(keys[1:])

					idx := 0
					for i, k := range keys {
						if cloned.ActiveProvider == k {
							idx = i
							break
						}
					}
					nextIdx := (idx + 1) % len(keys)
					cloned.ActiveProvider = keys[nextIdx]
					if cloned.ActiveProvider != "" {
						cloned.SyncActiveProvider()
					}
				},
			},
		}

		if cloned.ActiveProvider != "" {
			active := cloned.ActiveProvider
			items = append(items, &settingItem{
				id:          "provider_endpoint",
				name:        "endpoint url",
				value:       func() string { return cloned.Endpoint },
				description: fmt.Sprintf("API endpoint URL for active provider '%s'", active),
				onEdit: func(newVal string) error {
					if newVal == "" {
						return nil
					}
					cloned.Endpoint = newVal
					cloned.UpdateActiveProvider()
					return nil
				},
			})

			items = append(items, &settingItem{
				id:          "provider_apikey",
				name:        "api key",
				value:       func() string {
					if cloned.ApiKey == "" {
						return "none"
					}
					return "********"
				},
				description: fmt.Sprintf("API authorization key for active provider '%s'", active),
				onEdit: func(newVal string) error {
					cloned.ApiKey = newVal
					cloned.UpdateActiveProvider()
					return nil
				},
			})

			items = append(items, &settingItem{
				id:          "provider_model",
				name:        "model name",
				value:       func() string { return cloned.Model },
				description: fmt.Sprintf("Name of the LLM model to use for provider '%s'", active),
				onEdit: func(newVal string) error {
					if newVal == "" {
						return nil
					}
					cloned.Model = newVal
					cloned.UpdateActiveProvider()
					return nil
				},
			})
		}

		// Add custom actions
		items = append(items, &settingItem{
			id:          "action_add",
			name:        "[ add new provider ]",
			value:       func() string { return "" },
			description: "Add a new endpoint provider profile profile",
			onToggle: func() {
				fmt.Fprint(rlOutput, "\r\n\r\n  === Add New Endpoint Provider ===\r\n")

				fmt.Fprint(rlOutput, "  Enter provider name (e.g. brain, anthropic): ")
				pName, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				pName = strings.TrimSpace(pName)

				if pName != "" {
					fmt.Fprint(rlOutput, "  Enter API endpoint URL: ")
					pURL, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					pURL = strings.TrimSpace(pURL)

					if pURL != "" {
						fmt.Fprint(rlOutput, "  Enter API authorization key (optional): ")
						pKey, err := readInputRaw(rlInput, rlOutput)
						if err != nil {
							return
						}
						pKey = strings.TrimSpace(pKey)

						fmt.Fprint(rlOutput, "  Enter LLM model name (optional): ")
						pModel, err := readInputRaw(rlInput, rlOutput)
						if err != nil {
							return
						}
						pModel = strings.TrimSpace(pModel)

						if cloned.Providers == nil {
							cloned.Providers = make(map[string]config.ProviderConfig)
						}
						cloned.Providers[pName] = config.ProviderConfig{
							Name:     pName,
							Endpoint: pURL,
							ApiKey:   pKey,
							Model:    pModel,
						}
						cloned.ActiveProvider = pName
						cloned.SyncActiveProvider()
					}
				}
			},
		})

		items = append(items, &settingItem{
			id:          "action_remove",
			name:        "[ remove active provider ]",
			value:       func() string { return "" },
			description: "Delete the currently active endpoint provider profile",
			onToggle: func() {
				if cloned.ActiveProvider != "" {
					delete(cloned.Providers, cloned.ActiveProvider)
					cloned.ActiveProvider = ""
				}
			},
		})

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
		buf.WriteString(titleStyle.Render("endpoint providers config"))
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
					if item.id == "active_provider" || item.id == "action_add" || item.id == "action_remove" {
						if item.onToggle != nil {
							item.onToggle()
						}
					} else if item.onEdit != nil {
						fmt.Fprintf(rlOutput, "\r\n\r\n  edit %s (current: %s):\r\n", item.name, item.value())
						fmt.Fprint(rlOutput, "  enter new value: ")

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