package ui

import (
	"bytes"
	"testing"

	"bidouille/pkg/agent"
	"bidouille/pkg/config"
)

func TestParseManualCommand(t *testing.T) {
	tests := []struct {
		line          string
		enabled       bool
		expectedIsCmd bool
		expectedCmd   string
	}{
		// ! prefixed commands are always manual commands
		{"!git status", false, true, "git status"},
		{"!git status", true, true, "git status"},
		{"!ls -la", false, true, "ls -la"},

		// Direct commands enabled
		{"ls", true, true, "ls"},
		{"ls -la", true, true, "ls -la"},
		{"pwd", true, true, "pwd"},
		{"git status", true, true, "git status"},

		// Non-direct commands in new rule
		{"go build", true, false, ""},
		{"go run main.go", true, false, ""},
		{"mkdir src", true, true, "mkdir src"},
		{"find .", true, true, "find ."},

		// Direct commands disabled
		{"ls", false, false, ""},
		{"pwd", false, false, ""},
		{"git status", false, false, ""},

		// Non-direct commands
		{"echo hello", true, false, ""},
		{"vim file.txt", true, false, ""},
	}

	for _, tt := range tests {
		isCmd, cmdStr := parseManualCommand(tt.line, tt.enabled)
		if isCmd != tt.expectedIsCmd {
			t.Errorf("parseManualCommand(%q, %v) returned isCmd = %v; want %v", tt.line, tt.enabled, isCmd, tt.expectedIsCmd)
		}
		if cmdStr != tt.expectedCmd {
			t.Errorf("parseManualCommand(%q, %v) returned cmd = %q; want %q", tt.line, tt.enabled, cmdStr, tt.expectedCmd)
		}
	}
}

func TestCustomHistory(t *testing.T) {
	hist := &customHistory{}

	// Test adding items chronologically (oldest to newest)
	hist.Add("hi")
	hist.Add("sup ?")
	hist.Add("writea full stack api")

	if hist.Len() != 3 {
		t.Errorf("expected length 3, got %d", hist.Len())
	}

	// At(0) should be the newest/most recent
	if hist.At(0) != "writea full stack api" {
		t.Errorf("expected At(0) = 'writea full stack api', got %q", hist.At(0))
	}
	if hist.At(1) != "sup ?" {
		t.Errorf("expected At(1) = 'sup ?', got %q", hist.At(1))
	}
	if hist.At(2) != "hi" {
		t.Errorf("expected At(2) = 'hi', got %q", hist.At(2))
	}

	// Test duplicate entry - adding existing item should remove it from old position and move to front
	hist.Add("sup ?")
	if hist.Len() != 3 {
		t.Errorf("expected length after duplicate move to be 3, got %d", hist.Len())
	}
	if hist.At(0) != "sup ?" {
		t.Errorf("expected At(0) after duplicate add to be 'sup ?', got %q", hist.At(0))
	}
	if hist.At(1) != "writea full stack api" {
		t.Errorf("expected At(1) to be 'writea full stack api', got %q", hist.At(1))
	}
	if hist.At(2) != "hi" {
		t.Errorf("expected At(2) to be 'hi', got %q", hist.At(2))
	}

	// Test duplicate at index 0 - should do nothing and not change history length or order
	hist.Add("sup ?")
	if hist.Len() != 3 {
		t.Errorf("expected length to remain 3, got %d", hist.Len())
	}
	if hist.At(0) != "sup ?" {
		t.Errorf("expected At(0) to remain 'sup ?', got %q", hist.At(0))
	}
}

func TestDeduplicate(t *testing.T) {
	input := []string{"hi", "sup ?", "hi", "writea full stack api", "sup ?"}
	expected := []string{"hi", "writea full stack api", "sup ?"}

	result := Deduplicate(input)
	if len(result) != len(expected) {
		t.Fatalf("expected length %d, got %d", len(expected), len(result))
	}
	for i, val := range result {
		if val != expected[i] {
			t.Errorf("expected index %d to be %q, got %q", i, expected[i], val)
		}
	}
}

func TestKeyInterceptorReader(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			CollapseResults: false,
			ShowThinking:    true,
			ReasoningEffort: "low",
		},
	}

	var buf bytes.Buffer
	ki := &keyInterceptorReader{
		r:     bytes.NewReader([]byte{15, 20, 18}), // Ctrl+O, Ctrl+T, Ctrl+R
		agent: a,
		w:     &buf,
	}

	p := make([]byte, 1)

	// Read Ctrl+O (15)
	n, err := ki.Read(p)
	if err != nil {
		t.Fatalf("failed to read Ctrl+O: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes returned for Ctrl+O, got %d", n)
	}
	if !a.Config.CollapseResults {
		t.Errorf("expected CollapseResults to be true, got false")
	}

	// Read Ctrl+T (20)
	ki.r = bytes.NewReader([]byte{20})
	n, err = ki.Read(p)
	if err != nil {
		t.Fatalf("failed to read Ctrl+T: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes returned for Ctrl+T, got %d", n)
	}
	if a.Config.ShowThinking {
		t.Errorf("expected ShowThinking to be false, got true")
	}

	// Read Ctrl+R (18)
	ki.r = bytes.NewReader([]byte{18})
	n, err = ki.Read(p)
	if err != nil {
		t.Fatalf("failed to read Ctrl+R: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes returned for Ctrl+R, got %d", n)
	}
	if a.Config.ReasoningEffort != "medium" {
		t.Errorf("expected ReasoningEffort to be 'medium', got %q", a.Config.ReasoningEffort)
	}
}
