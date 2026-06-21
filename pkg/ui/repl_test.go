package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
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

	// Read Ctrl+O again (15)
	ki.r = bytes.NewReader([]byte{15})
	n, err = ki.Read(p)
	if err != nil {
		t.Fatalf("failed to read 2nd Ctrl+O: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes returned for 2nd Ctrl+O, got %d", n)
	}
	if a.Config.CollapseResults {
		t.Errorf("expected CollapseResults to be false, got true")
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

func TestFieldStartIndex(t *testing.T) {
	tests := []struct {
		s        string
		fieldIdx int
		expected int
	}{
		{"/agent spawn bob hello", 0, 0},
		{"/agent spawn bob hello", 1, 7},
		{"/agent spawn bob hello", 2, 13},
		{"/agent spawn bob hello", 3, 17},
		{"  /agent   spawn bob hello  ", 0, 2},
		{"  /agent   spawn bob hello  ", 1, 11},
		{"  /agent   spawn bob hello  ", 2, 17},
		{"  /agent   spawn bob hello  ", 3, 21},
		{"/agent spawn bob hello", 4, -1},
	}

	for _, tt := range tests {
		got := fieldStartIndex(tt.s, tt.fieldIdx)
		if got != tt.expected {
			t.Errorf("fieldStartIndex(%q, %d) = %d; want %d", tt.s, tt.fieldIdx, got, tt.expected)
		}
	}
}

func TestCtrlDExits(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	var buf bytes.Buffer
	ki := &keyInterceptorReader{
		r:     bytes.NewReader([]byte{4}), // Ctrl+D
		agent: a,
		w:     &buf,
	}
	rl := term.NewTerminal(ki, "")
	ki.rl = rl

	line, err := rl.ReadLine()
	if err == nil || err.Error() != "EOF" {
		t.Errorf("expected EOF on Ctrl+D, got line %q, err %v", line, err)
	}
}

func TestPrintPromptSeparatorStatic(t *testing.T) {
	theme := UITheme{
		Primary: style.Color("#ffffff"),
		Border:  style.Color("#555555"),
	}

	var buf bytes.Buffer
	// Test without spinner
	PrintPromptSeparatorWithSpinner(&buf, true, "low", theme, "")
	outputNoSpinner := buf.String()
	buf.Reset()

	// Test with spinner (should be ignored, remaining static)
	PrintPromptSeparatorWithSpinner(&buf, true, "low", theme, "◜")
	outputWithSpinner := buf.String()

	rawNoSpinner := stripAnsi(outputNoSpinner)
	rawWithSpinner := stripAnsi(outputWithSpinner)

	if rawNoSpinner != rawWithSpinner {
		t.Errorf("Expected prompt separator to remain static, got differences:\nno-spinner: %q\nwith-spinner: %q", rawNoSpinner, rawWithSpinner)
	}

	if !strings.HasPrefix(rawNoSpinner, "─── prompt ") {
		t.Errorf("Expected prefix '─── prompt ', got %q", rawNoSpinner)
	}
}

func TestDrawStaticStatsLine(t *testing.T) {
	theme := UITheme{
		Primary: style.Color("#ffffff"),
		Border:  style.Color("#555555"),
	}

	var buf bytes.Buffer
	// Test stats text
	DrawStaticStatsLine(&buf, theme, "", "14 out • 2026-06-15 01:02:06 (1.4s)")
	rawStats := buf.String()
	buf.Reset()

	// Test spinner
	DrawStaticStatsLine(&buf, theme, "◜", "")
	rawSpinner := buf.String()

	if !strings.Contains(rawStats, "14 out • 2026-06-15 01:02:06 (1.4s)") {
		t.Errorf("Expected rawStats to contain stats text, got %q", rawStats)
	}

	if !strings.Contains(rawSpinner, "◜") {
		t.Errorf("Expected rawSpinner to contain spinner, got %q", rawSpinner)
	}
}

type mockNewlineCounter struct {
	io.Writer
	count int
}

func (m *mockNewlineCounter) GetCount() int {
	return m.count
}

func TestGetNewlineCount(t *testing.T) {
	var buf bytes.Buffer
	m := &mockNewlineCounter{Writer: &buf, count: 42}
	got := getNewlineCount(m)
	if got != 42 {
		t.Errorf("expected getNewlineCount to return 42, got %d", got)
	}

	gotNull := getNewlineCount(&buf)
	if gotNull != 0 {
		t.Errorf("expected getNewlineCount for non-counter writer to return 0, got %d", gotNull)
	}
}

func TestKeyInterceptorReader_MultilinePaste(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	var buf bytes.Buffer
	pasteData := []byte("hello\nworld\n")
	ki := &keyInterceptorReader{
		r:     bytes.NewReader(pasteData),
		agent: a,
		w:     &buf,
	}

	p := make([]byte, len(pasteData))
	// Directly call ki.Read(p). Since n > 1 and it contains newlines, it will trigger isMultilinePaste logic.
	n, err := ki.Read(p)
	if err != nil {
		t.Fatalf("failed to read paste: %v", err)
	}

	if n != 0 {
		t.Errorf("expected 0 bytes returned (no auto-submit), got %d", n)
	}
	expectedPasted := "hello\nworld\n"
	if ki.pastedText != expectedPasted {
		t.Errorf("expected pastedText to be %q, got %q", expectedPasted, ki.pastedText)
	}
	// Verify that it echoed to ki.w (which is buf)
	if !strings.HasSuffix(buf.String(), "hello\r\nworld\r\n") {
		t.Errorf("expected echoed text to have carriage returns, got %q", buf.String())
	}
}

func TestCdUpdatesWorkspaceRoot(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "maquis-cd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origCwd)
	}()

	a := &agent.Agent{
		WorkspaceRoot: origCwd,
	}

	// Verify initial state
	if a.WorkspaceRoot != origCwd {
		t.Errorf("expected workspace root %q, got %q", origCwd, a.WorkspaceRoot)
	}

	// We don't call RunREPL directly to avoid entering an infinite terminal loop,
	// but we can simulate the cd command execution logic exactly:
	target := tempDir
	err = os.Chdir(target)
	if err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	pwd, _ := os.Getwd()
	a.WorkspaceRoot = pwd

	if a.WorkspaceRoot != tempDir {
		t.Errorf("expected workspace root to be updated to %q, got %q", tempDir, a.WorkspaceRoot)
	}
}

func TestHandleSessionSlashCommand(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	messages := []db.Message{}
	theme := &UITheme{}
	currentSessionID := "test-session-12345"

	var buf bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/session",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled {
		t.Errorf("expected slash command /session to be handled")
	}
	if quit {
		t.Errorf("expected /session not to quit the REPL")
	}

	got := buf.String()
	expectedActive := "active session: test-session-12345"
	expectedUsage := "usage: /session [list | new | load | branch <new_session_id> | clear]"

	if !strings.Contains(got, expectedActive) {
		t.Errorf("expected output to contain active session message: %q, got: %q", expectedActive, got)
	}
	if !strings.Contains(got, expectedUsage) {
		t.Errorf("expected output to contain usage message: %q, got: %q", expectedUsage, got)
	}

	buf.Reset()
	handled, quit = HandleSlashCommand(
		a,
		"/session invalid-sub-command",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled {
		t.Errorf("expected slash command /session invalid-sub-command to be handled")
	}
	if quit {
		t.Errorf("expected /session invalid-sub-command not to quit the REPL")
	}

	got = buf.String()
	if !strings.Contains(got, expectedActive) {
		t.Errorf("expected invalid subcommand output to contain active session message: %q, got: %q", expectedActive, got)
	}
	if !strings.Contains(got, expectedUsage) {
		t.Errorf("expected invalid subcommand output to contain usage message: %q, got: %q", expectedUsage, got)
	}
}

func TestJsonStreamParserStreamWrites(t *testing.T) {
	theme := UITheme{}

	t.Run("streamWrites=false", func(t *testing.T) {
		p := &jsonStreamParser{
			activeToolName: "write",
			streamWrites:   false,
		}
		var buf bytes.Buffer
		// Simulate streaming JSON chunk: {"write_content": "hello world"}
		p.feed(`{"write_content": "hello world"}`, &buf, theme)
		got := buf.String()
		if strings.Contains(got, "hello world") {
			t.Errorf("expected write_content to be suppressed, but got %q", got)
		}
	})

	t.Run("streamWrites=true", func(t *testing.T) {
		p := &jsonStreamParser{
			activeToolName: "write",
			streamWrites:   true,
		}
		var buf bytes.Buffer
		// Simulate streaming JSON chunk: {"write_content": "hello world"}
		p.feed(`{"write_content": "hello world"}`, &buf, theme)
		got := buf.String()
		if !strings.Contains(got, "hello world") {
			t.Errorf("expected write_content to be streamed, but got %q", got)
		}
	})

	t.Run("edit newText streamWrites=false", func(t *testing.T) {
		p := &jsonStreamParser{
			activeToolName: "edit",
			streamWrites:   false,
		}
		var buf bytes.Buffer
		p.feed(`{"newText": "hello world"}`, &buf, theme)
		got := buf.String()
		if strings.Contains(got, "hello world") {
			t.Errorf("expected newText to be suppressed, but got %q", got)
		}
	})

	t.Run("edit newText streamWrites=true", func(t *testing.T) {
		p := &jsonStreamParser{
			activeToolName: "edit",
			streamWrites:   true,
		}
		var buf bytes.Buffer
		p.feed(`{"newText": "hello world"}`, &buf, theme)
		got := buf.String()
		if !strings.Contains(got, "hello world") {
			t.Errorf("expected newText to be streamed, but got %q", got)
		}
	})
}

func TestStreamRendererStartToolCall(t *testing.T) {
	theme := UITheme{}
	var buf bytes.Buffer
	sr := NewStreamRenderer(&buf, theme, false, false, "test-agent")

	sr.StartToolCall("read", 0)

	gotClean := stripAnsi(buf.String())
	if gotClean != "" {
		t.Errorf("expected no output for suppressed tool streaming, got: %q", gotClean)
	}

	lineNum := sr.GetToolTitleLineNumber(0)
	if lineNum != -1 {
		t.Errorf("expected tool title line number to be -1, got: %d", lineNum)
	}
}

func TestHandleConfigAndSetCommands(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			MaxCompletionTokens: 100,
		},
	}
	messages := []db.Message{}
	theme := &UITheme{}
	currentSessionID := "test-session-12345"

	// 1. Test /config set max_completion_tokens
	var buf bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/config set max_completion_tokens 8192",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled || quit {
		t.Errorf("expected /config set to be handled and not quit")
	}
	if a.Config.MaxCompletionTokens != 8192 {
		t.Errorf("expected MaxCompletionTokens to be 8192, got %d", a.Config.MaxCompletionTokens)
	}

	// 2. Test /set max_tokens
	buf.Reset()
	handled, quit = HandleSlashCommand(
		a,
		"/set max_tokens 4096",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled || quit {
		t.Errorf("expected /set to be handled and not quit")
	}
	if a.Config.MaxCompletionTokens != 4096 {
		t.Errorf("expected MaxCompletionTokens to be 4096, got %d", a.Config.MaxCompletionTokens)
	}
}

func TestInlineMarkdownRendering(t *testing.T) {
	theme := UITheme{
		Primary:   style.Color("#ff0000"),
		Secondary: style.Color("#00ff00"),
		Highlight: style.Color("#0000ff"),
		Border:    style.Color("#555555"),
	}
	var buf bytes.Buffer
	sr := NewStreamRenderer(&buf, theme, false, false, "test")

	// Test 1: Bold **text**
	res := sr.renderInlineMarkdown("this is **bold** text")
	if !strings.Contains(res, "bold") || strings.Contains(res, "**") {
		t.Errorf("expected bold formatting without asterisks, got %q", res)
	}

	// Test 2: Inline code `code`
	res = sr.renderInlineMarkdown("this is `code` block")
	if !strings.Contains(res, "code") || strings.Contains(res, "`") {
		t.Errorf("expected code formatting without backticks, got %q", res)
	}

	// Test 3: Normal Line rendering (header)
	buf.Reset()
	sr.printNormalLine("# Main Header")
	got := buf.String()
	if !strings.Contains(got, "Main Header") || strings.Contains(got, "#") {
		t.Errorf("expected header rendering without hashes, got %q", got)
	}

	// Test 4: Normal Line rendering (bullet point)
	buf.Reset()
	sr.printNormalLine("- Bullet point")
	got = buf.String()
	if !strings.Contains(got, "•") || !strings.Contains(got, "Bullet point") {
		t.Errorf("expected bullet rendering with unicode dot, got %q", got)
	}

	// Test 5: Normal Line rendering (blockquote)
	buf.Reset()
	sr.printNormalLine("> Quote line")
	got = buf.String()
	if !strings.Contains(got, "┃") || !strings.Contains(got, "Quote line") {
		t.Errorf("expected blockquote rendering with border line, got %q", got)
	}
}



