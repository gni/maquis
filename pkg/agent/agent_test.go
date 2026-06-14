package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bidouille/pkg/agent/tool"
)


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

func TestFindToolGlob(t *testing.T) {
	tempDirRoot := os.Getenv("GOTMPDIR")
	if tempDirRoot == "" {
		tempDirRoot = os.TempDir()
	}
	tempDir, err := os.MkdirTemp(tempDirRoot, "bidouille-find-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create some files and directories
	os.MkdirAll(filepath.Join(tempDir, "backend", "alembic"), 0755)
	os.MkdirAll(filepath.Join(tempDir, "frontend", "src"), 0755)

	os.WriteFile(filepath.Join(tempDir, "backend", "main.py"), []byte("print('hello')"), 0644)
	os.WriteFile(filepath.Join(tempDir, "backend", "alembic", "env.py"), []byte("print('env')"), 0644)
	os.WriteFile(filepath.Join(tempDir, "frontend", "index.html"), []byte("html"), 0644)
	os.WriteFile(filepath.Join(tempDir, "frontend", "src", "App.vue"), []byte("vue"), 0644)

	a := &Agent{
		WorkspaceRoot: tempDir,
	}

	findExecutor := tool.NewFindTool()

	// Test 1: find *.py in workspace root
	res, err := findExecutor.Execute(a, `{"pattern": "*.py"}`)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(res, "backend/main.py") || !strings.Contains(res, "backend/alembic/env.py") {
		t.Errorf("expected backend/main.py and backend/alembic/env.py in results, got: %q", res)
	}

	// Test 2: find backend/**/*.py in workspace root
	res2, err := findExecutor.Execute(a, `{"pattern": "backend/**/*.py"}`)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(res2, "backend/main.py") || !strings.Contains(res2, "backend/alembic/env.py") {
		t.Errorf("expected both backend/main.py and backend/alembic/env.py in backend/**/*.py results, got: %q", res2)
	}

	// Test 3: find with path specified
	res3, err := findExecutor.Execute(a, `{"pattern": "*.py", "path": "backend"}`)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(res3, "backend/main.py") || !strings.Contains(res3, "backend/alembic/env.py") {
		t.Errorf("expected backend/main.py and backend/alembic/env.py in backend path results, got: %q", res3)
	}
}

func TestRecursiveLs(t *testing.T) {
	tempDirRoot := os.Getenv("GOTMPDIR")
	if tempDirRoot == "" {
		tempDirRoot = os.TempDir()
	}
	tempDir, err := os.MkdirTemp(tempDirRoot, "bidouille-ls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.MkdirAll(filepath.Join(tempDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tempDir, "file1.txt"), []byte("file1"), 0644)
	os.WriteFile(filepath.Join(tempDir, "subdir", "file2.txt"), []byte("file2"), 0644)

	a := &Agent{
		WorkspaceRoot: tempDir,
	}

	lsExecutor := tool.NewLsTool()

	// Non-recursive ls
	res, err := lsExecutor.Execute(a, `{"path": "."}`)
	if err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	if !strings.Contains(res, "file1.txt") || !strings.Contains(res, "subdir") || strings.Contains(res, "file2.txt") {
		t.Errorf("unexpected non-recursive ls output: %q", res)
	}

	// Recursive ls
	resRec, err := lsExecutor.Execute(a, `{"path": ".", "recursive": true}`)
	if err != nil {
		t.Fatalf("ls recursive failed: %v", err)
	}
	if !strings.Contains(resRec, "file1.txt") || !strings.Contains(resRec, "subdir/file2.txt") {
		t.Errorf("expected file1.txt and subdir/file2.txt in recursive ls output, got: %q", resRec)
	}
}



