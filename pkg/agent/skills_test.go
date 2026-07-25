package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsSubdirectorySKILLMD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "skills_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "loop-brain")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	skillFile := filepath.Join(subDir, "SKILL.md")
	content := `# Loop Brain Skill Guide
This is the loop brain instructions without frontmatter.
`
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	skills, err := LoadSkillsFromDirs(tmpDir)
	if err != nil {
		t.Fatalf("LoadSkillsFromDirs failed: %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	if skills[0].Name != "loop-brain" {
		t.Errorf("expected skill name 'loop-brain', got %q", skills[0].Name)
	}
}
