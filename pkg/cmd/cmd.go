package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui"
)

var (
	configPath          string
	endpoint            string
	modelName           string
	autoYes             bool
	showThinking        bool
	showTokens          bool
	allowedToolsStr     string
	sessionIDFlag       string
	maxStepsFlag        int
	resumeSession       bool
	reasoningEffortFlag string
	contextLimitFlag    int
	directCommandsFlag  bool
	maxCompletionTokensFlag int
	compactPrompt       bool
)

var rootCmd = &cobra.Command{
	Use:   "maquis [prompt]",
	Short: "maquis is a minimalist, resilient AI coding agent CLI.",
	Long:  `maquis is a Unix-style agent harness and interactive REPL that supports persistent session tracking, tool execution sandboxes, and advanced terminal visual themes.`,
	Args:  cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error loading configuration: %v\n", err)
			os.Exit(1)
		}

		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize session storage: %v\n", err)
		}

		// Apply flag overrides
		if endpoint != "" {
			cfg.Endpoint = endpoint
		}
		if modelName != "" {
			cfg.Model = modelName
		}
		if autoYes {
			cfg.AutoApprove = true
		}
		if showThinking {
			cfg.ShowThinking = true
		}
		if showTokens {
			cfg.ShowTokens = true
		}
		if maxStepsFlag != 0 {
			cfg.MaxReasoningSteps = maxStepsFlag
		}
		if reasoningEffortFlag != "" {
			cfg.ReasoningEffort = reasoningEffortFlag
		}
		if contextLimitFlag != 0 {
			cfg.ContextWindowLimit = contextLimitFlag
		}
		if cmd.Flags().Changed("direct") {
			cfg.DirectCommands = directCommandsFlag
		}
		if maxCompletionTokensFlag != 0 {
			cfg.MaxCompletionTokens = maxCompletionTokensFlag
		}
		if cmd.Flags().Changed("compact") {
			cfg.CompactPrompt = compactPrompt
		}

		theme := ui.GetConfiguredTheme(cfg)

		var allowedTools []string
		if allowedToolsStr != "" {
			allowedTools = strings.Split(allowedToolsStr, ",")
			for i, t := range allowedTools {
				allowedTools[i] = strings.TrimSpace(t)
			}
		}

		tlsConfig, err := config.GetTLSConfig(cfg)
		if err != nil {
			fmt.Printf("Warning: Failed to setup SSL certificates: %v. Proceeding without client certificates.\n", err)
			tlsConfig = nil
		}

		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}

		transport := &http.Transport{
			DialContext:           dialer.DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		
		if tlsConfig != nil {
			transport.TLSClientConfig = tlsConfig
		}

		httpClient := &http.Client{
			Transport: transport,
			Timeout:   0, // No timeout on long-running streaming responses
		}

		cwd, _ := os.Getwd()
		
		pipedData := ""
		if isPiped() {
			pipedData, _ = readStdin()
		}
		hasPromptArgs := len(args) > 0
		isNonInteractive := pipedData != "" || hasPromptArgs

		// Prompt for workspace trust if local executable extensions/plugins exist
		trustFile := filepath.Join(cwd, ".maquis-trust")
		trusted := false
		if _, err := os.Stat(trustFile); err == nil {
			trusted = true
		}

		pluginsDir := filepath.Join(cwd, "plugins")
		extensionsDir := filepath.Join(cwd, "extensions")
		hasLocalCode := false

		if info, err := os.Stat(pluginsDir); err == nil && info.IsDir() {
			if entries, _ := os.ReadDir(pluginsDir); len(entries) > 0 {
				hasLocalCode = true
			}
		}
		if info, err := os.Stat(extensionsDir); err == nil && info.IsDir() {
			if entries, _ := os.ReadDir(extensionsDir); len(entries) > 0 {
				hasLocalCode = true
			}
		}

		if hasLocalCode && !trusted && !isNonInteractive {
			fmt.Printf("\n\x1b[33m⚠️  Security Warning\x1b[0m: Workspace '%s' contains local plugins or extensions.\n", cwd)
			fmt.Printf("These can execute arbitrary code on your machine. Do you trust this workspace? [y/N]: ")
			var resp string
			fmt.Scanln(&resp)
			resp = strings.ToLower(strings.TrimSpace(resp))
			if resp == "y" || resp == "yes" {
				trusted = true
				_ = os.WriteFile(trustFile, []byte("trusted"), 0644)
				fmt.Println("\x1b[32mWorkspace trusted.\x1b[0m")
			}
		}

		if hasLocalCode && !trusted {
			if !isNonInteractive {
				fmt.Println("\x1b[31mLocal plugins and extensions have been DISABLED for this session.\x1b[0m")
			}
			cfg.DisableLocalPlugins = true
		}

		// Instantiate Agent context
		a := agent.NewAgent(cfg, configPath, httpClient)
		a.UI = ui.NewAgentUI(cfg, theme)

		// Load reference skills
		a.ActiveSkills, err = agent.LoadSkills(cfg.SkillsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
		}



		// Start MCP servers
		if len(cfg.MCPServers) > 0 {
			_ = a.StartMCPServers(cfg.MCPServers)
			defer a.StopMCPServers()

			if len(a.McpStartErrors) > 0 && isNonInteractive {
				ui.RenderMCPStartupErrors(os.Stderr, a.McpStartErrors, theme)
			}
		}

		sessionID := sessionIDFlag
		if sessionID == "" {
			if resumeSession {
				if lastID, err := db.GetLatestSessionID(); err == nil && lastID != "" {
					sessionID = lastID
				}
			}
			if sessionID == "" {
				sessionID = db.NewUUID()
			}
		}

		if pipedData != "" || hasPromptArgs {
			var promptBuilder strings.Builder
			if pipedData != "" {
				promptBuilder.WriteString("<stdin>\n")
				promptBuilder.WriteString(pipedData)
				promptBuilder.WriteString("\n</stdin>\n")
			}
			if hasPromptArgs {
				promptBuilder.WriteString(strings.Join(args, " "))
			}

			prompt := promptBuilder.String()
			var messages []db.Message
			if sessionID != "" {
				var err error
				messages, err = db.LoadMessages(sessionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to load session %s: %v\n", sessionID, err)
				}
			}
			if len(messages) == 0 {
				messages = []db.Message{
					{Role: "system", Content: a.GetSystemPrompt()},
				}
			}

			a.RunAgentLoop(context.Background(), os.Stdout, &messages, prompt, allowedTools, theme, true, sessionID)
			return
		}

		// Interactive REPL Mode
		ui.RunREPL(a, allowedTools, theme, sessionID)
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage maquis runtime settings",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Display the current configuration settings card",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		theme := ui.GetConfiguredTheme(cfg)
		ui.RenderConfig(os.Stdout, cfg, theme)
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the interactive TUI configuration editor",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		theme := ui.GetConfiguredTheme(cfg)

		newConfig, err := ui.RunInteractiveConfig(cfg, theme, os.Stdin, os.Stdout)
		if err == nil && newConfig != nil {
			_ = config.SaveConfig(configPath, newConfig)
			fmt.Printf("Configuration successfully updated and saved to %s\n", configPath)
		} else {
			fmt.Println("Interactive configuration cancelled.")
		}
	},
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage persistent conversation sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all saved conversation sessions and metadata",
	Run: func(cmd *cobra.Command, args []string) {
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		sessions, err := db.GetSessions()
		if err != nil || len(sessions) == 0 {
			fmt.Println("No past sessions found.")
			return
		}

		fmt.Println("Past Conversation Sessions:")
		for _, s := range sessions {
			preview := s.Preview
			if len(preview) > 50 {
				preview = preview[:50] + "..."
			}
			fmt.Printf("  - %s [%s] (%d messages) - %s\n", s.SessionID, s.Timestamp[:16], s.MsgCount, preview)
		}
	},
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Start a new session and output its UUID",
	Run: func(cmd *cobra.Command, args []string) {
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		newID := db.NewUUID()
		cfg, _ := config.LoadConfig(configPath)
		a := agent.NewAgent(cfg, configPath, nil)
		sysMsg := db.Message{Role: "system", Content: a.GetSystemPrompt()}
		_ = db.ClearSession(newID)
		_ = db.SaveMessage(newID, sysMsg)

		fmt.Println(newID)
	},
}

var sessionClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all saved conversation sessions from disk",
	Run: func(cmd *cobra.Command, args []string) {
		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		if err := db.ClearHistory(); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Println("All saved conversation sessions cleared from disk.")
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func isPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

func readStdin() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
func init() {
	defaultConfig := "config/config.json"
	home, err := os.UserHomeDir()
	if err == nil {
		defaultConfig = filepath.Join(home, ".maquis", "config.json")
	}
	rootCmd.SetHelpCommand(&cobra.Command{Use: "no-help-command", Hidden: true})
	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "Path to config JSON file")
	rootCmd.PersistentFlags().StringVar(&endpoint, "endpoint", "", "Override llama.cpp or OpenAI server URL")
	rootCmd.PersistentFlags().StringVar(&modelName, "model", "", "Override model name")
	rootCmd.PersistentFlags().BoolVarP(&autoYes, "yes", "y", false, "Auto-approve all tool execution prompts without asking")
	rootCmd.PersistentFlags().BoolVar(&showThinking, "thinking", false, "Show streaming LLM thinking/reasoning process")
	rootCmd.PersistentFlags().BoolVarP(&showTokens, "tokens", "t", false, "Show token usage metrics under each LLM response")
	rootCmd.PersistentFlags().StringVar(&allowedToolsStr, "tools", "", "Comma-separated list of allowed tools (leave empty for all)")
	rootCmd.PersistentFlags().StringVarP(&sessionIDFlag, "session", "s", "", "Resume a specific persistent conversation session ID")
	rootCmd.PersistentFlags().BoolVarP(&resumeSession, "resume", "r", false, "Resume the latest conversation session instead of starting a new one")
	rootCmd.PersistentFlags().StringVar(&reasoningEffortFlag, "reasoning", "", "Override LLM reasoning effort (e.g. low, medium, high)")
	rootCmd.PersistentFlags().IntVar(&maxStepsFlag, "steps", 0, "Override maximum reasoning steps limit (e.g. 30)")
	rootCmd.PersistentFlags().IntVar(&contextLimitFlag, "context-limit", 0, "Override context window limit (default: 128000)")
	rootCmd.PersistentFlags().IntVar(&maxCompletionTokensFlag, "max-completion-tokens", 0, "Override maximum completion/output tokens limit (default: 16384)")
	rootCmd.PersistentFlags().BoolVar(&directCommandsFlag, "direct", false, "Enable direct execution of local shell commands (default: true in config)")
	rootCmd.PersistentFlags().BoolVar(&compactPrompt, "compact", false, "Enable highly compressed system instructions for smaller models")

	configCmd.AddCommand(configShowCmd, configEditCmd)
	sessionCmd.AddCommand(sessionListCmd, sessionNewCmd, sessionClearCmd)
	rootCmd.AddCommand(configCmd, sessionCmd)
}
