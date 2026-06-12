package agent

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"bidouille/pkg/agent/tool"
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
		{"mkdir src", true, false, ""},
		{"find .", true, false, ""},

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

func TestSafePath(t *testing.T) {
	a := &Agent{
		WorkspaceRoot: "/workspace/bidouille",
	}

	home, _ := os.UserHomeDir()

	tests := []struct {
		inputPath string
		expectErr bool
		expected  string
	}{
		{"pkg/agent/loop.go", false, "/workspace/bidouille/pkg/agent/loop.go"},
		{"/workspace/bidouille/pkg/agent/loop.go", false, "/workspace/bidouille/pkg/agent/loop.go"},
		{"../loop.go", true, ""},
		{"/etc/passwd", true, ""},
		{"../../../etc/passwd", true, ""},
		{"", false, "/workspace/bidouille"},
		{".", false, "/workspace/bidouille"},
		{home + "/.bidouille/BIDOUILLE.md", false, home + "/.bidouille/BIDOUILLE.md"},
		{home + "/some_other_file.txt", true, ""},
	}

	for _, tt := range tests {
		got, err := a.SafePath(tt.inputPath)
		if (err != nil) != tt.expectErr {
			t.Errorf("SafePath(%q) error status = %v; want error? %v", tt.inputPath, err, tt.expectErr)
			continue
		}
		if !tt.expectErr && got != tt.expected {
			t.Errorf("SafePath(%q) = %q; want %q", tt.inputPath, got, tt.expected)
		}
	}
}

func TestOmissionPlaceholders(t *testing.T) {
	tests := []struct {
		input  string
		expect bool
	}{
		{"func main() {\n  // ... rest of code ...\n}", true},
		{"func main() {\n  // TODO: implement this later\n}", false},
		{"func main() {\n  // TODO ...\n}", true},
		{"func main() {\n  // unchanged code ...\n}", true},
		{"func main() {\n  /* rest of methods ... */\n}", true},
		{"func main() {\n  # rest of code ...\n}", true},
		{"func main() {\n  // normal comment\n}", false},
	}

	for i, tt := range tests {
		matches := tool.DetectOmissionPlaceholders(tt.input)
		got := len(matches) > 0
		if got != tt.expect {
			t.Errorf("Test %d failed for input %q: got %v, want %v. Matches: %v", i, tt.input, got, tt.expect, matches)
		}
	}
}

func TestBackgroundTask(t *testing.T) {
	tempDirRoot := os.Getenv("GOTMPDIR")
	if tempDirRoot == "" {
		tempDirRoot = os.TempDir()
	}
	tempDir, err := os.MkdirTemp(tempDirRoot, "bidouille-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	a := &Agent{
		WorkspaceRoot: tempDir,
		Tasks:         make(map[string]*Task),
		NextTaskId:    1,
	}

	var buf bytes.Buffer
	id, err := a.SpawnTask("sleep 10", &buf)
	if err != nil {
		t.Fatalf("failed to spawn task: %v", err)
	}

	if id != "task_1" {
		t.Errorf("expected task ID 'task_1', got %q", id)
	}

	// Wait briefly for the task to be marked running/started
	time.Sleep(100 * time.Millisecond)

	status, _, err := a.GetTaskStatus(id)
	if err != nil {
		t.Fatalf("failed to get task status: %v", err)
	}
	if status != "running" {
		t.Errorf("expected status 'running', got %q", status)
	}

	list := a.ListTasks()
	if len(list) != 1 {
		t.Errorf("expected 1 task in list, got %d", len(list))
	} else {
		if list[0].ID != id {
			t.Errorf("expected task ID %q in list, got %q", id, list[0].ID)
		}
		if list[0].Status != "running" {
			t.Errorf("expected task status 'running' in list, got %q", list[0].Status)
		}
	}

	err = a.KillTask(id)
	if err != nil {
		t.Fatalf("failed to kill task: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	status, _, err = a.GetTaskStatus(id)
	if err != nil {
		t.Fatalf("failed to get task status after kill: %v", err)
	}
	if status != "killed" {
		t.Errorf("expected status 'killed', got %q", status)
	}

	id2, err := a.SpawnTask("echo 'hello bidouille'", &buf)
	if err != nil {
		t.Fatalf("failed to spawn second task: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	status2, output2, err := a.GetTaskStatus(id2)
	if err != nil {
		t.Fatalf("failed to get second task status: %v", err)
	}
	if status2 != "completed" {
		t.Errorf("expected second task status 'completed', got %q", status2)
	}
	if !strings.Contains(output2, "hello bidouille") {
		t.Errorf("expected output to contain 'hello bidouille', got %q", output2)
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

func TestResilientEdit(t *testing.T) {
	tempDirRoot := os.Getenv("GOTMPDIR")
	if tempDirRoot == "" {
		tempDirRoot = os.TempDir()
	}
	tempDir, err := os.MkdirTemp(tempDirRoot, "bidouille-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	a := &Agent{
		WorkspaceRoot: tempDir,
	}

	testFilePath := "test_file.py"
	fullPath, err := a.SafePath(testFilePath)
	if err != nil {
		t.Fatalf("failed to get safe path: %v", err)
	}

	fileContent := `def hello():
    print("hello")
    
    # some comment
    return True

def world():
    print("world")
`

	err = os.WriteFile(fullPath, []byte(fileContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tTool := tool.NewEditTool()

	// Case 1: Exact match
	argsJson := `{"path": "test_file.py", "updates": [{"oldText": "def hello():\n    print(\"hello\")", "newText": "def hello():\n    print(\"hello, exact\")"}]}`
	_, err = tTool.Execute(a, argsJson)
	if err != nil {
		t.Fatalf("Case 1 (exact match) failed: %v", err)
	}

	// Read content and check
	contentBytes, _ := os.ReadFile(fullPath)
	content := string(contentBytes)
	if !strings.Contains(content, "hello, exact") {
		t.Errorf("Case 1 expected 'hello, exact' in file content, got: %s", content)
	}

	// Reset file content
	os.WriteFile(fullPath, []byte(fileContent), 0644)

	// Case 2: Indentation discrepancy (e.g. 2 spaces instead of 4)
	argsJson = `{"path": "test_file.py", "updates": [{"oldText": "def hello():\n  print(\"hello\")\n  \n  # some comment\n  return True", "newText": "def hello():\n    print(\"hello, resilient\")\n    \n    # some comment\n    return True"}]}`
	_, err = tTool.Execute(a, argsJson)
	if err != nil {
		t.Fatalf("Case 2 (resilient match) failed: %v", err)
	}

	contentBytes, _ = os.ReadFile(fullPath)
	content = string(contentBytes)
	if !strings.Contains(content, "hello, resilient") {
		t.Errorf("Case 2 expected 'hello, resilient' in file content, got: %s", content)
	}

	// Reset file content
	os.WriteFile(fullPath, []byte(fileContent), 0644)

	// Case 3: CRLF in file or request
	fileContentCRLF := "def hello():\r\n    print(\"hello\")\r\n"
	os.WriteFile(fullPath, []byte(fileContentCRLF), 0644)
	argsJson = `{"path": "test_file.py", "updates": [{"oldText": "def hello():\n    print(\"hello\")", "newText": "def hello():\n    print(\"hello, crlf\")"}]}`
	_, err = tTool.Execute(a, argsJson)
	if err != nil {
		t.Fatalf("Case 3 (CRLF) failed: %v", err)
	}

	contentBytes, _ = os.ReadFile(fullPath)
	content = string(contentBytes)
	if !strings.Contains(content, "hello, crlf") {
		t.Errorf("Case 3 expected 'hello, crlf' in file content, got: %s", content)
	}

	// Reset file content
	os.WriteFile(fullPath, []byte(fileContent), 0644)

	// Case 4: Request has leading/trailing newlines/spaces
	argsJson = `{"path": "test_file.py", "updates": [{"oldText": "\n\ndef hello():\n  print(\"hello\")\n\n\n", "newText": "def hello():\n    print(\"hello, trimmed\")"}]}`
	_, err = tTool.Execute(a, argsJson)
	if err != nil {
		t.Fatalf("Case 4 (trimmed newlines) failed: %v", err)
	}

	contentBytes, _ = os.ReadFile(fullPath)
	content = string(contentBytes)
	if !strings.Contains(content, "hello, trimmed") {
		t.Errorf("Case 4 expected 'hello, trimmed' in file content, got: %s", content)
	}

	// Reset file content
	os.WriteFile(fullPath, []byte(fileContent), 0644)

	// Case 5: Ambiguous / multiple matches should fail
	fileContentAmbiguous := `def hello():
    print("hello")

def other():
    print("hello")
`
	os.WriteFile(fullPath, []byte(fileContentAmbiguous), 0644)
	argsJson = `{"path": "test_file.py", "updates": [{"oldText": "print(\"hello\")", "newText": "print(\"hello, duplicate\")"}]}`
	_, err = tTool.Execute(a, argsJson)
	if err == nil {
		t.Fatalf("Case 5 (ambiguous match) expected to fail but succeeded")
	}
	if !strings.Contains(err.Error(), "is not unique") {
		t.Errorf("Case 5 expected uniqueness error, got: %v", err)
	}
}

func TestKeyInterceptorReader(t *testing.T) {
	a := &Agent{
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


