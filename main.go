package main

import (
	_ "embed"
	"fmt"
	"os"

	"bidouille/pkg/cmd"
	"bidouille/pkg/config"
)

//go:embed config/config.json
var defaultTemplate []byte

func main() {
	config.SetDefaultTemplate(defaultTemplate)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
