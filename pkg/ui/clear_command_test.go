package ui

import (
	"bytes"
	"strings"
	"testing"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
)

func TestClearCommandRebuildsOneCanonicalFrame(t *testing.T) {
	if err := db.InitDB(t.TempDir()); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}

	a := &agent.Agent{
		Config: &config.Config{
			ContextWindowLimit: 128000,
			Endpoint:           "https://example.invalid",
			Model:              "test-model",
		},
	}
	messages := []db.Message{
		{Role: "system", Content: "old system prompt"},
		{Role: "user", Content: "old conversation content"},
	}
	theme := &UITheme{}
	sessionID := "clear-frame-test"
	var output bytes.Buffer
	ki := &keyInterceptorReader{
		agent:    a,
		w:        &output,
		messages: &messages,
	}

	handled, quit := HandleSlashCommand(
		a,
		"/clear",
		&messages,
		nil,
		theme,
		&output,
		&sessionID,
		nil,
		nil,
		ki,
	)

	if !handled || quit {
		t.Fatalf("HandleSlashCommand(/clear) = handled %v, quit %v; want true, false", handled, quit)
	}

	rendered := output.String()
	if !strings.Contains(rendered, "\x1b[r\x1b[H\x1b[J") {
		t.Fatal("/clear did not emit an atomic clear-and-redraw frame")
	}
	if got := strings.Count(rendered, "maquis v1.0.0"); got != 1 {
		t.Fatalf("/clear rendered %d banners; want exactly 1", got)
	}
	if !strings.Contains(rendered, "conversation cleared and started a new one.") {
		t.Fatal("/clear omitted its completion notice")
	}
	if strings.Contains(rendered, "old conversation content") {
		t.Fatal("/clear retained stale conversation content in the rebuilt frame")
	}
	if len(messages) != 1 || messages[0].Role != "system" {
		t.Fatalf("/clear retained unexpected messages: %#v", messages)
	}
}
