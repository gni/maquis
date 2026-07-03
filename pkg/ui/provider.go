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
		fmt.Fprint(w, "\x1b[H")
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

	itemsProvider := func() []*settingItem {
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
				},
			},
		}

		// Sort providers and add sub-settings
		var pNames []string
		for p := range cloned.Providers {
			pNames = append(pNames, p)
		}
		sort.Strings(pNames)

		for _, pn := range pNames {
			pName := pn
			cfgProv := cloned.Providers[pName]

			items = append(items, &settingItem{
				id:          "prov_endpoint_" + pName,
				name:        "provider: " + pName,
				value:       func() string { return cfgProv.Endpoint },
				description: fmt.Sprintf("Base API endpoint URL for '%s'", pName),
				onEdit: func(newVal string) error {
					if newVal == "" {
						return fmt.Errorf("endpoint URL cannot be empty")
					}
					curr := cloned.Providers[pName]
					curr.Endpoint = newVal
					cloned.Providers[pName] = curr
					return nil
				},
			})

			items = append(items, &settingItem{
				id:          "prov_key_" + pName,
				name:        "  api key",
				value: func() string {
					key := cfgProv.ApiKey
					if key == "" {
						return "(not set)"
					}
					if len(key) <= 8 {
						return "********"
					}
					return key[:4] + "..." + key[len(key)-4:] + " (masked)"
				},
				description: fmt.Sprintf("API Key credential for '%s'", pName),
				onEdit: func(newVal string) error {
					curr := cloned.Providers[pName]
					curr.ApiKey = newVal
					cloned.Providers[pName] = curr
					return nil
				},
			})
		}

		// Action items
		items = append(items, &settingItem{
			id:          "action_add",
			name:        "[ add new provider ]",
			value:       func() string { return "" },
			description: "Configure a custom provider endpoint (OpenAI API spec compatible)",
			onToggle: func() {
				fmt.Fprint(rlOutput, "\r\n\r\n  === Add Custom Provider ===\r\n")
				fmt.Fprint(rlOutput, "  Enter provider name: ")
				pName, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				pName = strings.TrimSpace(pName)
				if pName != "" {
					fmt.Fprint(rlOutput, "  Enter base endpoint URL: ")
					pURL, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					pURL = strings.TrimSpace(pURL)
					if pURL != "" {
						if cloned.Providers == nil {
							cloned.Providers = make(map[string]config.ProviderConfig)
						}
						cloned.Providers[pName] = config.ProviderConfig{
							Endpoint: pURL,
						}
						cloned.ActiveProvider = pName
					}
				}
			},
		})

		if cloned.ActiveProvider != "" {
			actName := cloned.ActiveProvider
			items = append(items, &settingItem{
				id:          "action_remove",
				name:        "[ remove active provider ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Remove the provider configuration for '%s'", actName),
				onToggle: func() {
					delete(cloned.Providers, actName)
					cloned.ActiveProvider = ""
				},
			})
		}

		return items
	}

	err = runSettingsMenuLoop(rlInput, rlOutput, theme, "endpoint providers config", itemsProvider, nil)
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}