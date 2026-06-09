# Bidouille: Minimalist AI Coding Agent Harness

Bidouille is a terminal-based AI agent harness written in Go. It executes stateful agentic loops using OpenAI-compatible APIs and standard filesystem/execution tools.

---

## Codebase Architecture

The codebase is organized into modules under the `pkg/` directory:

*   **`cmd`**: Command-line interface definitions using Cobra. Manages flags, subcommands (`config`, `session`), environment overrides, and stream detection (redirecting TTY inputs for tool approval when stdin is piped).
*   **`config`**: Application configuration struct, defaults loading, JSON serialization, environment variable fallbacks, and mutual TLS (mTLS) client certificate configuration.
*   **`db`**: Session database layer using JSON Lines (`.jsonl`). Handles reading and appending message structs, enumerating past sessions, computing token counts, and sorting conversation history.
*   **`agent`**: Orchestrates the core agent loop and capability tools:
    *   `llm.go`: API payload construction, prompt engineering, streaming response ingestion, and reasoning effort controls.
    *   `loop.go`: Agent execution loop, token usage parsing, hooks execution (`BeforeToolHook`/`AfterToolHook`), and step limit constraints.
    *   `tools.go`: Builtin tool definitions, parameters schema validation, and tool allowlist filters.
    *   `skills.go`: Loader for specialized system prompts/skills files.
    *   `mcp.go`: Model Context Protocol client manager for running external tool servers.
*   **`ui`**: Console renderer. Formats Markdown outputs, highlights syntax inside code blocks, displays spinner animations, and hosts the interactive TUI configuration menu.

---

## Configuration Schema

The configuration parameters are serialized as a JSON object at `config.json`. Below is the Go structural definition:

```go
type Config struct {
	Endpoint             string                     `json:"endpoint"`
	ApiKey               string                     `json:"api_key,omitempty"`
	Model                string                     `json:"model"`
	Temperature          float64                    `json:"temperature"`
	SystemInstruction    string                     `json:"system_instruction"`
	AutoApprove          bool                       `json:"auto_approve,omitempty"`
	ShowThinking         bool                       `json:"show_thinking"`
	ShowFullThinking     bool                       `json:"show_full_thinking"`
	CollapseResults      bool                       `json:"collapse_results"`
	ShowTokens           bool                       `json:"show_tokens"`
	Theme                string                     `json:"theme"`
	CertFile             string                     `json:"cert_file,omitempty"`
	KeyFile              string                     `json:"key_file,omitempty"`
	CAFile               string                     `json:"ca_file,omitempty"`
	SkipVerify           bool                       `json:"skip_verify"`
	SkillsDir            string                     `json:"skills_dir"`
	MCPServers           map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	MaxReasoningSteps    int                        `json:"max_reasoning_steps"`
	ContextWindowLimit   int                        `json:"context_window_limit"`
	CompressionThreshold float64                    `json:"compression_threshold"`
	ReasoningEffort      string                     `json:"reasoning_effort,omitempty"`
	BeforeToolHook       string                     `json:"before_tool_hook,omitempty"`
	AfterToolHook        string                     `json:"after_tool_hook,omitempty"`
}
```

### Available Themes

The `theme` parameter accepts the following visual styles (all designed to minimize eye strain):

*   **`dark`** (default): Nord Dark theme utilizing soft arctic blues, lavender, snow white, and muted slate grays.
*   **`gruvbox`**: A warm retro/earthy palette with soft aqua, pink, warm sand, and dark warm grays.
*   **`neon`**: A soft cyberpunk/pastel theme (Dracula-inspired) with muted cyan, pink, and off-white.
*   **`light`**: Solarized Light style featuring soft blue, magenta, dark slate text, and silver borders.

---

## CLI Reference

### Subcommands

*   **`config show`**: Displays the active configuration file parameters.
*   **`config edit`**: Launches the interactive TUI configuration editor.
*   **`session list`**: Lists metadata (UUID, message count, timestamp, preview) of all stored JSONL conversation files.
*   **`session new`**: Initializes a new session log and prints its UUID.

### Command-Line Flags

| Flag | Shorthand | Description |
| :--- | :--- | :--- |
| `--config` | | Specifies path to `config.json` (defaults to `~/.bidouille/config.json`). |
| `--endpoint` | | Overrides the target OpenAI-compatible API base URL. |
| `--model` | | Overrides the target API model identifier. |
| `--yes` | `-y` | Enables auto-approval of tool execution commands. |
| `--thinking` | | Toggles streaming display of the LLM's reasoning tokens. |
| `--tokens` | `-t` | Appends token usage statistics to each output block. |
| `--tools` | | Comma-separated allowlist of registered tools to enable. |
| `--session` | `-s` | Resumes a conversation from the specified session ID. |
| `--resume` | `-r` | Resumes the most recently modified session. |
| `--reasoning`| | Selects reasoning effort level (`low`, `medium`, `high`). |
| `--steps` | | Maximum number of tool execution steps allowed per query. |

---

## Registered Tools

The client exposes the following tools to the agent:

1.  **`bash(command string)`**: Executes a terminal command in the workspace directory.
2.  **`read(path string, offset int, limit int)`**: Reads lines of a file starting at `offset` up to `limit`.
3.  **`write(path string, write_content string)`**: Overwrites or creates a file with the given content.
4.  **`edit(path string, updates []ReplaceEdit)`**: Replaces matching text blocks in a file. `ReplaceEdit` contains `oldText` and `newText`.
5.  **`grep(pattern string, path string, glob string, ignoreCase bool, literal bool, limit int)`**: Searches for text matching `pattern` recursively.
6.  **`find(pattern string, path string, limit int)`**: Lists files matching the glob `pattern`.
7.  **`ls(path string)`**: Lists files and directories in the target path.
8.  **`load_skill(name string)`**: Loads system prompt/instructions for a specific skill from the skills directory.

---

## Build and Compilation

The project requires Go version 1.21 or later.

```bash
# Tidy and download dependencies
go mod tidy

# Build the executable
go build -o bidouille
```
