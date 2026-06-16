package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"maquis/pkg/config"
	"maquis/pkg/ui"
)

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Manage endpoint provider profiles",
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured endpoint providers",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		theme := ui.GetConfiguredTheme(cfg)
		ui.RenderProviders(os.Stdout, cfg, theme)
	},
}

var providerAddCmd = &cobra.Command{
	Use:   "add <name> <url> [key] [model]",
	Short: "Add a new endpoint provider profile",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		name := args[0]
		url := args[1]
		key := ""
		if len(args) > 2 {
			key = args[2]
		}
		model := ""
		if len(args) > 3 {
			model = strings.Join(args[3:], " ")
		}

		if cfg.Providers == nil {
			cfg.Providers = make(map[string]config.ProviderConfig)
		}
		cfg.Providers[name] = config.ProviderConfig{
			Name:     name,
			Endpoint: url,
			ApiKey:   key,
			Model:    model,
		}
		err = config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("Provider '%s' successfully added/updated.\n", name)
	},
}

var providerSelectCmd = &cobra.Command{
	Use:   "select <name>",
	Short: "Select active endpoint provider",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		name := args[0]
		if name == "none" || name == "default" {
			cfg.ActiveProvider = ""
			_ = config.SaveConfig(configPath, cfg)
			fmt.Println("Switched active provider to default settings.")
			return
		}

		if cfg.Providers == nil {
			cfg.Providers = make(map[string]config.ProviderConfig)
		}
		if _, ok := cfg.Providers[name]; !ok {
			fmt.Printf("Error: provider '%s' not found.\n", name)
			return
		}

		cfg.ActiveProvider = name
		cfg.SyncActiveProvider()
		err = config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("Switched active provider to '%s'.\n", name)
	},
}

var providerModelCmd = &cobra.Command{
	Use:   "model <model>",
	Short: "Set model for the currently active endpoint provider",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		model := strings.Join(args, " ")
		cfg.Model = model
		cfg.UpdateActiveProvider()
		err = config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		if cfg.ActiveProvider != "" {
			fmt.Printf("Updated model for provider '%s' to '%s'.\n", cfg.ActiveProvider, model)
		} else {
			fmt.Printf("Updated default model to '%s'.\n", model)
		}
	},
}

var providerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an endpoint provider profile",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		name := args[0]
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]config.ProviderConfig)
		}
		if _, ok := cfg.Providers[name]; !ok {
			fmt.Printf("Error: provider '%s' not found.\n", name)
			return
		}

		delete(cfg.Providers, name)
		if cfg.ActiveProvider == name {
			cfg.ActiveProvider = ""
		}
		err = config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("Provider '%s' removed.\n", name)
	},
}

func init() {
	providerCmd.AddCommand(providerListCmd, providerAddCmd, providerSelectCmd, providerModelCmd, providerRemoveCmd)
	rootCmd.AddCommand(providerCmd)
}
