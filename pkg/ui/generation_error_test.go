package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"maquis/pkg/config"
	"maquis/pkg/db"
)

func TestGenerationErrorRenderingSanitizesAndBoundsServerBody(t *testing.T) {
	message := "after 3 retries: server returned non-200 status: 502. Body: <html>\r\n" +
		"<head><title>502 Bad Gateway</title></head>\r\n" +
		"<body>\x1b[2J\x1b[H<center><h1>502 Bad Gateway</h1></center></body>\r\n" +
		"</html>" + strings.Repeat("x", 120)

	var output bytes.Buffer
	PrintSessionHistory(
		&output,
		[]db.Message{{Role: "error", Content: message}},
		UITheme{},
		&config.Config{},
	)

	rendered := output.String()
	if strings.ContainsRune(rendered, '\r') {
		t.Fatal("generation error output retained a carriage return")
	}
	if strings.Contains(rendered, "\x1b[2J") || strings.Contains(rendered, "\x1b[H") {
		t.Fatal("generation error output retained terminal control sequences from the server body")
	}

	plain := strings.TrimRight(stripAnsi(rendered), "\n")
	if !strings.Contains(plain, "502 Bad Gateway") {
		t.Fatalf("generation error output lost the diagnostic body: %q", plain)
	}

	lines := strings.Split(plain, "\n")
	if len(lines) < 3 {
		t.Fatalf("generation error output did not render a bordered block: %q", plain)
	}
	width := utf8.RuneCountInString(lines[0])
	if width > 80 {
		t.Fatalf("generation error width = %d; want at most 80", width)
	}
	for i, line := range lines {
		if got := utf8.RuneCountInString(line); got != width {
			t.Fatalf("generation error line %d width = %d; want %d\n%s", i, got, width, plain)
		}
	}
}
