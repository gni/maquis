package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"maquis/pkg/cmd"
	"maquis/pkg/config"
	"maquis/pkg/ui"
)

//go:embed config/config.json
var defaultTemplate []byte

//go:embed config/providers.json
var defaultProvidersTemplate []byte

//go:embed config/mcp.json
var defaultMCPTemplate []byte

func main() {
	config.SetDefaultTemplate(defaultTemplate, defaultProvidersTemplate, defaultMCPTemplate)

	// Restore cursor on startup in case a previous crashed run left it hidden
	fmt.Fprint(os.Stderr, "\x1b[?25h")

	// Intercept termination signals to cleanly restore cursor and scroll margins
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		var lastInterrupt time.Time
		for sig := range sigChan {
			if sig == os.Interrupt {
				now := time.Now()
				// If Ctrl+C is pressed twice within 1.5 seconds, force exit
				if now.Sub(lastInterrupt) < 1500*time.Millisecond {
					ui.ShutdownStatusBar(os.Stderr)
					os.Exit(130)
				}
				lastInterrupt = now

				if ui.IsInteractive {
					if ui.CancelActiveOperation() {
						continue
					}
					// If there is no active operation to cancel, exit immediately
					ui.ShutdownStatusBar(os.Stderr)
					os.Exit(130)
				}
			}
			ui.ShutdownStatusBar(os.Stderr)
			os.Exit(0)
		}
	}()

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
