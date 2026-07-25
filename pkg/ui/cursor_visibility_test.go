package ui

import (
	"bytes"
	"strings"
	"testing"

	"maquis/pkg/agent"
	"maquis/pkg/config"
)

func useIsolatedCursorTestUI(t *testing.T) {
	t.Helper()

	previous := ActiveUI
	ActiveUI = &AgentUIImpl{}
	t.Cleanup(func() {
		ActiveUI = previous
	})
}

func TestPromptPreservingWriterKeepsCursorModeStableWhileStreaming(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 30)
	writer.SetPromptCol(5)
	output.Reset()

	if _, err := writer.Write([]byte("token")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	if strings.Contains(got, "\x1b[?25") {
		t.Fatalf("Write() changed cursor visibility during streaming: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[28;5H") {
		t.Fatalf("Write() did not restore the cursor to the prompt: %q", got)
	}
}

func TestRedrawTypeAheadLeavesCursorAtLiveInputPosition(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 24)
	a := &agent.Agent{
		Config:        &config.Config{},
		CurrentWriter: writer,
	}
	reader := &keyInterceptorReader{
		agent:           a,
		w:               &output,
		typeAheadBuffer: []byte("hello"),
	}

	reader.redrawTypeAhead()

	got := output.String()
	if strings.Contains(got, "\x1b[?25") {
		t.Fatalf("redrawTypeAhead() changed cursor visibility: %q", got)
	}
	if strings.Contains(got, "\x1b7") || strings.Contains(got, "\x1b8") {
		t.Fatalf("redrawTypeAhead() restored the cursor away from live input: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[22;8H") {
		t.Fatalf("redrawTypeAhead() did not leave the cursor after the input: %q", got)
	}
	if writer.promptCol != 8 {
		t.Fatalf("prompt column = %d; want 8", writer.promptCol)
	}
}
