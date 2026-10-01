package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maquis/pkg/ui/style"

	"maquis/pkg/agent/tool"
)

type Skill = tool.Skill

var ActiveSkills []Skill

func ParseFrontmatter(content string) (map[string]string, string) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return nil, content
	}

	rest := strings.TrimPrefix(trimmed, "---")
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	endIdx := -1
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return nil, content
	}

	fmLines := lines[:endIdx]
	bodyLines := lines[endIdx+1:]
	body := strings.Join(bodyLines, "\n")

	fm := make(map[string]string)
	for _, line := range fmLines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) == 2 {
			val := strings.TrimSpace(kv[1])
			val = strings.Trim(val, `"'`)
			fm[strings.TrimSpace(kv[0])] = val
		}
	}
	return fm, body
}

func LoadSkillsFromDirs(dirs ...string) ([]Skill, error) {
	var skills []Skill
	seen := make(map[string]bool)

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if strings.HasPrefix(dir, "~/") {
			homeDir, _ := os.UserHomeDir()
			dir = filepath.Join(homeDir, dir[2:])
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			absDir = dir
		}
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}

		_ = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.ToLower(filepath.Ext(path)) != ".md" {
				return nil
			}

			rel, err := filepath.Rel(absDir, path)
			if err != nil {
				return nil
			}
			relParts := strings.Split(filepath.ToSlash(rel), "/")
			baseName := info.Name()
			baseNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

			// Top-level skill file: skills/my-skill.md (relParts length 1)
			// Or skill package root: skills/my-skill/SKILL.md (relParts length 2)
			// Ignore deeper sub-docs (e.g. skills/my-skill/references/notes.md)
			if len(relParts) > 2 && !strings.EqualFold(baseNoExt, "SKILL") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			fm, body := ParseFrontmatter(string(data))
			name := ""
			desc := ""
			if fm != nil {
				name = fm["name"]
				desc = fm["description"]
			}

			if name == "" {
				if strings.EqualFold(baseNoExt, "SKILL") || strings.EqualFold(baseNoExt, "README") {
					name = filepath.Base(filepath.Dir(path))
				} else {
					name = baseNoExt
				}
			}

			if desc == "" {
				lines := strings.Split(strings.TrimSpace(body), "\n")
				for _, l := range lines {
					l = strings.TrimSpace(l)
					if l != "" && !strings.HasPrefix(l, "---") {
						desc = strings.TrimPrefix(l, "# ")
						break
					}
				}
				if desc == "" {
					desc = name + " skill guide"
				}
			}

			if !seen[name] {
				seen[name] = true
				skills = append(skills, Skill{
					Name:        name,
					Description: desc,
					Path:        path,
					Content:     strings.TrimSpace(body),
				})
			}
			return nil
		})
	}
	return skills, nil
}

func LoadSkills(skillsDir string) ([]Skill, error) {
	cwd, _ := os.Getwd()
	dirs := []string{
		skillsDir,
		filepath.Join(cwd, "skills"),
		filepath.Join(cwd, ".agents", "skills"),
	}
	return LoadSkillsFromDirs(dirs...)
}

func subagentSkillGuidance(skills []Skill) string {
	names := make([]string, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("\n\nSubagent skill assignment:\n")
	if len(names) == 0 {
		sb.WriteString("- No registered reference skills are currently installed.\n")
	} else {
		sb.WriteString("- Registered reference skill names: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString(".\n")
	}
	sb.WriteString("- Use skill_names only for exact registered names. Do not invent reference skill names.\n")
	sb.WriteString("- For a new specialization, define it in the subagent system_prompt or provide inline_skills. Unknown skill_names are preserved as agent-local skills using that subagent's system_prompt.\n")
	return sb.String()
}

func RenderSkills(w io.Writer, skills []Skill, theme style.UITheme) {
	if len(skills) == 0 {
		fmt.Fprintln(w, "No reference skills found.")
		return
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	pathStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)

	fmt.Fprintln(w, headerStyle.Render("╭──────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  AVAILABLE SKILLS (Reference Guides)                                             │"))
	fmt.Fprintln(w, headerStyle.Render("├──────────────────────────────────────────────────────────────────────────────────┤"))

	for _, skill := range skills {
		fmt.Fprintf(w, "  %s - %s\n", titleStyle.Render(skill.Name), descStyle.Render(skill.Description))
		fmt.Fprintf(w, "  %s\n\n", pathStyle.Render(skill.Path))
	}
	fmt.Fprintln(w, headerStyle.Render("╰──────────────────────────────────────────────────────────────────────────────────╯"))
}

// SystemPromptConfig encapsulates all parameters required to generate a
// consistent, deterministic system prompt across single and multi-agent roles.
type SystemPromptConfig struct {
	BaseInstruction string
	WorkspaceRoot   string
	SkillsDir       string
	CompactPrompt   bool
	ActiveAgents    []string
	Skills          []tool.Skill
	AllSkills       []tool.Skill
	MemoryContext   string
	ActiveTasks     []TaskInfo
}

// BuildSystemPrompt constructs the complete system prompt instructions, guidelines,
// skill catalog, and multi-agent topology guidance using structured XML sections.
func BuildSystemPrompt(cfg SystemPromptConfig) string {
	workspaceRoot := cfg.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = "."
	}

	skillsDir := cfg.SkillsDir
	if skillsDir == "" {
		skillsDir = "skills"
	}

	skillsForGuidance := cfg.Skills
	if len(cfg.AllSkills) > 0 {
		skillsForGuidance = cfg.AllSkills
	}

	baseInstruction := cfg.BaseInstruction
	if idx := strings.Index(baseInstruction, "\nGuidelines:\n"); idx != -1 {
		baseInstruction = strings.TrimSpace(baseInstruction[:idx])
	}
	baseInstruction = strings.TrimSpace(baseInstruction)
	if baseInstruction == "" {
		baseInstruction = "You are maquis, an elite autonomous software engineering harness. You solve engineering tasks with senior craft, architectural rigor, and direct working deliverables."
	}

	var sb strings.Builder

	// 1. Preamble section
	sb.WriteString(baseInstruction)

	// 2. Tools section
	sb.WriteString("\n\n<tools>\n")
	sb.WriteString("- read: Read file contents\n")
	sb.WriteString("- edit: Make precise file edits with exact text replacement\n")
	sb.WriteString("- write: Create or overwrite complete files\n")
	sb.WriteString("- bash: Execute shell commands (builds, tests, git, background processes)\n")
	sb.WriteString("- grep: Search file contents for patterns or regular expressions\n")
	sb.WriteString("- find: Find files by glob pattern\n")
	sb.WriteString("- list: List directory contents\n")
	if len(cfg.ActiveAgents) > 0 {
		for _, name := range cfg.ActiveAgents {
			sb.WriteString(fmt.Sprintf("- subagent__%s: Delegate task to specialized subagent '%s'\n", name, name))
		}
	}
	sb.WriteString("</tools>\n\n")

	// 3. Rules section
	sb.WriteString("<rules>\n")
	sb.WriteString(fmt.Sprintf("- Workspace root: `%s`. Any relative file paths resolve relative to this directory.\n", workspaceRoot))
	sb.WriteString("- Actions over talk: Implement code on disk directly using write and edit tools. Deliver complete working code.\n")
	sb.WriteString("- Tool Selection: Use 'read' to examine specific files instead of cat or sed in bash. Never call 'read' on a directory path; use 'list' to inspect directory trees, and call 'read' with the exact file path (e.g. 'src/core/errors/__init__.py' rather than 'src/core/errors'). Use 'find' to locate files by glob pattern, and 'grep' to search definitions or references across the workspace.\n")
	sb.WriteString("- Search Discipline: Never run search pipelines (find, grep, xargs) in bash. Limit searches to targeted queries using 'grep' or 'find'. If a directory listing or search returns no files related to the requested topic, conclude the search immediately and answer from internal knowledge.\n")
	sb.WriteString("- Knowledge Fallback: When asked about recipes, domain questions, explanations, or concepts not present in workspace files, synthesize the answer directly from your knowledge base. Do NOT enter loops repeatedly listing directories, reading unrelated files, or attempting to spawn helper agents.\n")
	sb.WriteString("- Precise Edits: Keep oldText minimal (2-5 lines). Before editing a file, read it first to verify its content. If edit reports an oldText mismatch, read the latest file and retry a smaller exact unique block. Never recover by overwriting the existing file with write.\n")
	if len(cfg.ActiveAgents) > 0 {
		sb.WriteString("- Subagents: When the user asks to call or use agents, or to delegate duties, use 'spawn_subagent' to spawn specialized agents and delegate tasks to them.\n")
	}
	sb.WriteString("- Background Processes: When asked to run a command or service in the background, use the 'bash' tool with \"background\": true.\n")
	sb.WriteString("- Be concise and direct in your responses.\n")
	sb.WriteString("</rules>")

	// 4. Skills section
	if len(cfg.Skills) > 0 {
		sb.WriteString("\n\n<skills>\n")
		sb.WriteString(fmt.Sprintf("Installed reference skills are stored in `%s`. Use the 'load_skill' tool to read detailed instructions:\n", skillsDir))
		for _, s := range cfg.Skills {
			sb.WriteString(fmt.Sprintf("- name: %s\n  description: %s\n", s.Name, s.Description))
		}
		sb.WriteString("</skills>")
	}

	sb.WriteString(subagentSkillGuidance(skillsForGuidance))

	// 5. Swarm info if subagents active (in non-compact mode)
	if !cfg.CompactPrompt && len(cfg.ActiveAgents) > 0 {
		sb.WriteString(fmt.Sprintf("\n\n<subagents>\nActive spawned subagents in the swarm: %s.\nDelegate subtasks by calling 'subagent__<name>' with the task prompt.\n</subagents>", strings.Join(cfg.ActiveAgents, ", ")))
	}

	if cfg.MemoryContext != "" {
		sb.WriteString(cfg.MemoryContext)
	}

	// 6. Background tasks section if running tasks exist
	if len(cfg.ActiveTasks) > 0 {
		sb.WriteString("\n\n<background_tasks>\n")
		sb.WriteString("Active running background tasks in this session:\n")
		for _, t := range cfg.ActiveTasks {
			sb.WriteString(fmt.Sprintf("- %s: status=%s duration=%v command=`%s`\n", t.ID, t.Status, t.Duration.Round(time.Second), t.Command))
		}
		sb.WriteString("Use 'task_status' with task_id to inspect output or 'task_kill' to terminate a task.\n")
		sb.WriteString("</background_tasks>")
	}

	// 7. CWD section
	sb.WriteString(fmt.Sprintf("\n\n<cwd>\n%s\n</cwd>", workspaceRoot))

	return sb.String()
}

func (a *Agent) GetSystemPrompt() string {
	var activeAgents []string
	a.SpawnedAgentsMu.RLock()
	for name := range a.SpawnedAgents {
		activeAgents = append(activeAgents, name)
	}
	a.SpawnedAgentsMu.RUnlock()
	sort.Strings(activeAgents)

	skillsDir := "skills"
	compact := false
	instruction := ""
	if a.Config != nil {
		skillsDir = a.Config.SkillsDir
		compact = a.Config.CompactPrompt
		instruction = a.Config.SystemInstruction
	}

	var activeTasks []TaskInfo
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks = append(activeTasks, t)
		}
	}

	return BuildSystemPrompt(SystemPromptConfig{
		BaseInstruction: instruction,
		WorkspaceRoot:   a.WorkspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		ActiveAgents:    activeAgents,
		Skills:          a.ActiveSkills,
		MemoryContext:   a.LoadMemoryContext(),
		ActiveTasks:     activeTasks,
	})
}
