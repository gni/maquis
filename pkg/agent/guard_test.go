package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestGuard_PerFileReadLimit(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"
	args := `{"path":"` + file + `"}`

	for i := 1; i <= 3; i++ {
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("iteration %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content of settings.py", nil)
	}

	// 4th read of same file should be blocked before execution
	err := guard.CheckPreExecution("read", args)
	if err == nil {
		t.Fatalf("expected 4th read of %s to be blocked, but it was allowed", file)
	}
	if !strings.Contains(err.Error(), "has already been inspected 3 times") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "Call 'edit' or 'write' now") {
		t.Fatalf("error message missing actionable directive: %v", err)
	}
}

func TestGuard_DistinctFileInspectionsAllowed(t *testing.T) {
	guard := NewTurnExecutionGuard()

	// Inspecting 20 distinct files and directories across the workspace should NEVER be blocked
	for i := 1; i <= 20; i++ {
		args := `{"path":"src/file_` + string(rune('a'+i)) + `.go"}`
		if err := guard.CheckPreExecution("read", args); err != nil {
			t.Fatalf("distinct file inspection %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("read", args, "content", nil)
	}

	// Listing distinct directories should also be allowed
	dirs := []string{"src/core", "src/modules", "src/core/agents", "src/core/crypto", "src/core/errors", "src/core/types"}
	for _, d := range dirs {
		args := `{"path":"` + d + `"}`
		if err := guard.CheckPreExecution("list", args); err != nil {
			t.Fatalf("distinct directory list '%s' unexpectedly blocked: %v", d, err)
		}
		guard.RecordPostExecution("list", args, "entries", nil)
	}
}

func TestGuard_DirectoryInspectionLimit(t *testing.T) {
	guard := NewTurnExecutionGuard()
	dir := "src/core/errors"
	args := `{"path":"` + dir + `"}`

	for i := 1; i <= 3; i++ {
		if err := guard.CheckPreExecution("list", args); err != nil {
			t.Fatalf("list %d unexpectedly blocked: %v", i, err)
		}
		guard.RecordPostExecution("list", args, "entries", nil)
	}

	// 4th list of same directory should be blocked
	err := guard.CheckPreExecution("list", args)
	if err == nil {
		t.Fatalf("expected 4th list of %s to be blocked", dir)
	}
	if !strings.Contains(err.Error(), "has already been inspected 3 times") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGuard_BashFileInspectionExtraction(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"

	// 1. read via tool 'read'
	readArgs := `{"path":"` + file + `"}`
	if err := guard.CheckPreExecution("read", readArgs); err != nil {
		t.Fatalf("read unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("read", readArgs, "content", nil)

	// 2. read via bash 'cat'
	catArgs := `{"command":"cat src/config/settings.py"}`
	if err := guard.CheckPreExecution("bash", catArgs); err != nil {
		t.Fatalf("cat unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("bash", catArgs, "content", nil)

	// 3. read via python script with pathlib
	pyArgs := `{"command":"python3 -c \"import pathlib; print(pathlib.Path('src/config/settings.py').read_text())\""}`
	if err := guard.CheckPreExecution("bash", pyArgs); err != nil {
		t.Fatalf("python read unexpectedly blocked: %v", err)
	}
	guard.RecordPostExecution("bash", pyArgs, "content", nil)

	if count := guard.FileReadCount(file); count != 3 {
		t.Fatalf("expected file read count 3, got %d", count)
	}

	// 4. A 4th inspection via bash should now be blocked
	err := guard.CheckPreExecution("bash", catArgs)
	if err == nil {
		t.Fatalf("expected 4th inspection via bash cat to be blocked")
	}
	if !strings.Contains(err.Error(), "has already been inspected 3 times") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGuard_ModificationResetsCounters(t *testing.T) {
	guard := NewTurnExecutionGuard()
	file := "src/config/settings.py"
	args := `{"path":"` + file + `"}`

	// Read 3 times
	for i := 0; i < 3; i++ {
		_ = guard.CheckPreExecution("read", args)
		guard.RecordPostExecution("read", args, "content", nil)
	}

	// 4th read is blocked
	if err := guard.CheckPreExecution("read", args); err == nil {
		t.Fatalf("expected 4th read to be blocked before modification")
	}

	// Apply an edit
	editArgs := `{"path":"` + file + `","oldText":"foo","newText":"bar"}`
	guard.RecordPostExecution("edit", editArgs, "successfully edited", nil)

	if guard.FileReadCount(file) != 0 {
		t.Fatalf("expected file read count reset to 0, got %d", guard.FileReadCount(file))
	}

	// Now reading the file again is allowed
	if err := guard.CheckPreExecution("read", args); err != nil {
		t.Fatalf("reading file after edit was unexpectedly blocked: %v", err)
	}
}

func TestGuard_RepeatedFailingBashCommand(t *testing.T) {
	guard := NewTurnExecutionGuard()
	failingCmd := `{"command":"python3 -c \"import paththlib\""}`

	// 1st failure
	if err := guard.CheckPreExecution("bash", failingCmd); err != nil {
		t.Fatalf("1st attempt blocked: %v", err)
	}
	guard.RecordPostExecution("bash", failingCmd, "NameError", errors.New("command failed: exit status 1"))

	// 2nd failure
	if err := guard.CheckPreExecution("bash", failingCmd); err != nil {
		t.Fatalf("2nd attempt blocked: %v", err)
	}
	guard.RecordPostExecution("bash", failingCmd, "NameError", errors.New("command failed: exit status 1"))

	// 3rd attempt should be blocked before execution
	err := guard.CheckPreExecution("bash", failingCmd)
	if err == nil {
		t.Fatalf("expected repeatedly failing command to be blocked")
	}
	if !strings.Contains(err.Error(), "has failed repeatedly (2 times)") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Different command should NOT be blocked
	differentCmd := `{"command":"python3 -c \"import pathlib\""}`
	if err := guard.CheckPreExecution("bash", differentCmd); err != nil {
		t.Fatalf("different command unexpectedly blocked: %v", err)
	}
}

func TestGuard_ConsecutiveIdenticalCalls(t *testing.T) {
	guard := NewTurnExecutionGuard()
	cmd := `{"command":"pytest tests/"}`

	for i := 1; i <= 3; i++ {
		if err := guard.CheckPreExecution("bash", cmd); err != nil {
			t.Fatalf("attempt %d unexpectedly blocked: %v", i, err)
		}
		// Succeeded (err == nil) but identical
		guard.RecordPostExecution("bash", cmd, "tests passed", nil)
	}

	// 4th identical execution should be blocked
	err := guard.CheckPreExecution("bash", cmd)
	if err == nil {
		t.Fatalf("expected 4th identical call to be blocked")
	}
	if !strings.Contains(err.Error(), "loop detected: identical tool call repeated 3 times") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
