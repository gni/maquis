package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"maquis/pkg/cmd"
	"maquis/pkg/config"
	"maquis/pkg/ui"
)

//go:embed config/config.json
var defaultTemplate []byte

func main() {
	config.SetDefaultTemplate(defaultTemplate)

	// Restore cursor on startup in case a previous crashed run left it hidden
	fmt.Fprint(os.Stderr, "\x1b[?25h")

	// Intercept termination signals to cleanly restore cursor and scroll margins
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		ui.ShutdownStatusBar(os.Stderr)
		os.Exit(0)
	}()

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
