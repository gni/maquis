package ui

import (
	"bytes"
	"strings"
	"testing"

	"maquis/pkg/agent"
	"maquis/pkg/config"
)

func TestPrintBannerIcons(t *testing.T) {
	cfg := &config.Config{
		Endpoint: "http://localhost:11434",
		Model:    "llama3:latest",
	}
	a := agent.NewAgent(cfg, "", nil)

	var buf bytes.Buffer
	PrintBanner(&buf, a)

	output := buf.String()
	if !strings.Contains(output, "maquis v1.0.0") {
		t.Errorf("expected banner to contain 'maquis v1.0.0', got: %s", output)
	}

	// When pluginsCount == 0 and extensionsCount == 0, zero tag should be hidden
	if strings.Contains(output, "⊞") || strings.Contains(output, "⌁") {
		t.Errorf("expected zero plugin/extension tag to be hidden, got: %s", output)
	}
}
