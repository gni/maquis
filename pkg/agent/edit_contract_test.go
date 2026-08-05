package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
)

func TestEditTreatsUniqueDesiredBlockAsAlreadyApplied(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "auth.py")
	const current = "def login():\n    return \"already updated\"\n"
	if err := os.WriteFile(path, []byte(current), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := &Agent{WorkspaceRoot: workspace}
	executor := tool.NewEditTool()
	output, err := executor.Execute(
		a,
		`{"path":"auth.py","updates":[{"oldText":"def login():\n    return \"old\"","newText":"def login():\n    return \"already updated\""}]}`,
	)
	if err != nil {
		t.Fatalf("already-applied edit returned an error: %v", err)
	}
	if !strings.Contains(output, "already present") {
		t.Fatalf("already-applied edit omitted its no-op result: %q", output)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(after) != current {
		t.Fatalf("already-applied edit changed the file:\ngot:  %q\nwant: %q", string(after), current)
	}
}

func TestCompactEditSchemaRetainsFreshExactMatchContract(t *testing.T) {
	editDefinition := compressToolDefinition(tool.NewEditTool().Definition())
	for _, expected := range []string{"exact", "latest read", "smaller block"} {
		if !strings.Contains(strings.ToLower(editDefinition.Function.Description), expected) {
			t.Fatalf("compact edit description omitted %q: %q", expected, editDefinition.Function.Description)
		}
	}

	updates := editDefinition.Function.Parameters.Properties["updates"]
	oldText := updates.Items.Properties["oldText"].Description
	for _, expected := range []string{"exact", "unique", "latest read"} {
		if !strings.Contains(strings.ToLower(oldText), expected) {
			t.Fatalf("compact oldText description omitted %q: %q", expected, oldText)
		}
	}

	writeDefinition := compressToolDefinition(tool.NewWriteTool().Definition())
	if !strings.Contains(strings.ToLower(writeDefinition.Function.Description), "never use after an edit mismatch") {
		t.Fatalf("compact write description permits unsafe edit fallback: %q", writeDefinition.Function.Description)
	}
}

func TestCompactPromptForbidsWholeFileWriteAfterEditMismatch(t *testing.T) {
	a := &Agent{
		Config: &config.Config{
			CompactPrompt:     true,
			SystemInstruction: "test agent",
			SkillsDir:         t.TempDir(),
		},
		WorkspaceRoot: t.TempDir(),
	}

	prompt := strings.ToLower(a.GetSystemPrompt())
	for _, expected := range []string{"oldtext mismatch", "smaller exact unique block", "never recover"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("compact system prompt omitted %q", expected)
		}
	}
}

func TestEditMismatchProvidesClosestLineRecommendation(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "DashboardLayout.tsx")
	const current = "// Header\nimport React from 'react';\n\nexport const DashboardLayout = () => {\n  return <div>Dashboard</div>;\n};\n"
	if err := os.WriteFile(path, []byte(current), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := &Agent{WorkspaceRoot: workspace}
	executor := tool.NewEditTool()
	_, err := executor.Execute(
		a,
		`{"path":"DashboardLayout.tsx","updates":[{"oldText":"export const DashboardLayout = () => {\n  return <span>Old</span>;\n}","newText":"export const DashboardLayout = () => {\n  return <div>New</div>;\n}"}]}`,
	)
	if err == nil {
		t.Fatalf("expected error on mismatched edit, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "oldText block was not found in file") {
		t.Fatalf("expected oldText error prefix, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "around line 4") {
		t.Fatalf("expected recommendation pointing to around line 4, got: %s", errMsg)
	}
}

func TestEditFuzzyBlockMatching(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "gallery.html")
	const current = "<div class=\"gallery\">\n  <header class=\"title\">Header</header>\n  <main class=\"content\">Main Body</main>\n  <footer class=\"footer\">Footer</footer>\n</div>\n"
	if err := os.WriteFile(path, []byte(current), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := &Agent{WorkspaceRoot: workspace}
	executor := tool.NewEditTool()
	// Slight discrepancy on line 2 (header vs title)
	_, err := executor.Execute(
		a,
		`{"path":"gallery.html","updates":[{"oldText":"<div class=\"gallery\">\n  <header class=\"header\">Header</header>\n  <main class=\"content\">Main Body</main>\n  <footer class=\"footer\">Footer</footer>\n</div>","newText":"<div class=\"gallery\">\n  <header class=\"new\">Header</header>\n  <main class=\"content\">Main Body</main>\n  <footer class=\"footer\">Footer</footer>\n</div>"}]}`,
	)
	if err != nil {
		t.Fatalf("expected fuzzy match to succeed, got error: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "header class=\"new\"") {
		t.Fatalf("expected file to contain updated content, got: %s", string(after))
	}
}
