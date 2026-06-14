# bidouille

bidouille is a minimalist terminal-based AI agent harness written in Go. It executes stateful agentic loops using OpenAI-compatible APIs and standard filesystem/execution tools.

This project is tested only on llama.cpp.

## Installation

Building from source requires Go 1.21 or later.

```bash
go mod tidy
go build -o bidouille
```

## Usage

### Configuration

Bidouille stores configuration settings in ~/.bidouille/config.json. The file is created automatically on the first execution.

To configure Bidouille for llama.cpp:

1. Start your llama.cpp server:
```bash
./llama-server -m models/your-model.gguf -c 4096 --port 8080
```

2. Edit the configuration to point to the server endpoint:
```bash
./bidouille config edit
```
Alternatively, edit ~/.bidouille/config.json manually:
```json
{
  "endpoint": "http://localhost:8080/v1",
  "api_key": "none",
  "model": "your-model-name",
  "theme": "dark"
}
```

Settings can also be overridden per-session using CLI flags.

### CLI Commands

Start an interactive session:
```bash
./bidouille
```

Start a session with an initial prompt:
```bash
./bidouille "Write a quicksort implementation in Go"
```

Start an interactive session overriding the endpoint and model:
```bash
./bidouille --endpoint http://localhost:8080/v1 --model your-model-name
```

Automatically approve all tool execution prompts:
```bash
./bidouille --yes
```

Manage saved sessions:
```bash
# List past sessions
./bidouille session list

# Start a new session and display its UUID
./bidouille session new

# Resume the most recently modified session
./bidouille --resume

# Resume a specific session by ID
./bidouille --session <session-uuid>
```

Display and manage configuration settings:
```bash
# Show configuration settings
./bidouille config show

# Edit configuration settings
./bidouille config edit
```

### CLI Flags

- --config <path>: Path to config JSON file (defaults to ~/.bidouille/config.json)
- --endpoint <url>: Override llama.cpp or OpenAI server URL
- --model <name>: Override model name
- -y, --yes: Auto-approve all tool execution prompts without asking
- --thinking: Show streaming LLM thinking/reasoning process
- -t, --tokens: Show token usage metrics under each LLM response
- --tools <list>: Comma-separated list of allowed tools (leave empty for all)
- -s, --session <uuid>: Resume a specific persistent conversation session ID
- -r, --resume: Resume the latest conversation session instead of starting a new one
- --reasoning <level>: Override LLM reasoning effort (low, medium, high)
- --steps <limit>: Override maximum reasoning steps limit (default: 30)
- --context-limit <limit>: Override context window limit (default: 128000)

## Tools

The agent can invoke the following built-in tools:

- bash(command): Executes a command in the terminal.
- read(path, offset, limit): Reads a text file within line boundaries.
- write(path, write_content): Creates or overwrites a file.
- edit(path, updates): Applies search-and-replace replacements to a file.
- grep(pattern, path, glob, ignoreCase, literal, limit): Searches for text patterns.
- find(pattern, path, limit): Finds files matching a name pattern.
- ls(path): Lists directory contents.
- load_skill(name): Loads prompt instructions for a specific skill from the skills directory.
- task_status(task_id): Retrieves the execution status and buffered output of a background task.
- task_kill(task_id): Terminates a running background task.

## Memory Context

bidouille automatically loads context from global and project-level memory files, appending them directly to the system prompt of the agent:

- Global Memory: ~/.bidouille/BIDOUILLE.md (for global settings or agent personalization)
- Project Memory: MEMORY.md or .bidouille/MEMORY.md in the current workspace or git directory (for project-specific logs, state, and instructions)

## Author

Lucian BLETAN

## License

MIT License
