# maquis

maquis is a minimalist terminal-based AI agent harness written in Go. It executes stateful agentic loops using OpenAI-compatible APIs and standard filesystem/execution tools.

This project is tested only on llama.cpp.
Don't use this on production, still experimental
Agent designed to consume tokens, UI may broke

## Installation

Building from source requires Go 1.21 or later.

```bash
go mod tidy
go build -o maquis
```

Less dependencies for more security..
```bash
require (
	github.com/alecthomas/chroma/v2 v2.14.0
	github.com/spf13/cobra v1.10.2
	golang.org/x/sys v0.44.0
	golang.org/x/term v0.43.0
)

require (
	github.com/dlclark/regexp2 v1.11.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
```


## Usage

### Configuration

maquis stores configuration settings in ~/.maquis/config.json. The file is created automatically on the first execution.

To configure maquis for llama.cpp:

1. Start your llama.cpp server:
```bash
./llama-server -m models/your-model.gguf -c 4096 --port 8080
```

2. Edit the configuration to point to the server endpoint:
```bash
./maquis config edit
```
Alternatively, edit ~/.maquis/config.json manually:
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
./maquis
```

Start a session with an initial prompt:
```bash
./maquis "Write a quicksort implementation in Go"
```

Start an interactive session overriding the endpoint and model:
```bash
./maquis --endpoint http://localhost:8080/v1 --model your-model-name
```

Automatically approve all tool execution prompts:
```bash
./maquis --yes
```

Manage saved sessions:
```bash
# List past sessions
./maquis session list

# Start a new session and display its UUID
./maquis session new

# Clear all saved conversation sessions from disk
./maquis session clear

# Resume the most recently modified session
./maquis --resume

# Resume a specific session by ID
./maquis --session <session-uuid>
```

Display and manage configuration settings:
```bash
# Show configuration settings
./maquis config show

# Edit configuration settings
./maquis config edit
```

### CLI Flags

- --config <path>: Path to config JSON file (defaults to ~/.maquis/config.json)
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

maquis automatically loads context from global and project-level memory files, appending them directly to the system prompt of the agent:

- Global Memory: ~/.maquis/MAQUIS.md (for global settings or agent personalization)
- Project Memory: MEMORY.md or .maquis/MEMORY.md in the current workspace or git directory (for project-specific logs, state, and instructions)

## Plugins

Maquis supports loading custom plugins written in any scripting or compiled language. Executable files placed in the plugins folders are automatically discovered and registered as tools on startup.

To list all loaded plugin tools, type `/plugins` in the REPL. To dynamically reload all custom plugins after making changes to your scripts, type `/reload` in the REPL.

### Plugin Folders

- Global Plugins: ~/.maquis/plugins/
- Project Plugins: plugins/ (in the workspace root)

### Writing a Plugin

1. Create a script or binary (e.g. `plugins/my_tool.py` or `plugins/my_tool.sh`) and make it executable (`chmod +x`).
2. Implement `--info` command line option: When executed with `--info`, your plugin must output a JSON representation of its function tool definition to `stdout`.
3. Implement execution handler: When called by the agent, Maquis executes the script directly and writes the parameters JSON string to `stdin`. Your script should print its execution results to `stdout`.

#### Example Python Plugin (plugins/hello_tool.py)

```python
#!/usr/bin/env python3
import sys
import json

# Output registration info when queried
if len(sys.argv) > 1 and sys.argv[1] == "--info":
    info = {
        "name": "hello_tool",
        "description": "Say hello to a user with a custom greeting",
        "parameters": {
            "type": "object",
            "properties": {
                "name": {"type": "string", "description": "The name of the user to greet"}
            },
            "required": ["name"]
        }
    }
    print(json.dumps(info))
    sys.exit(0)

# Otherwise, execute the tool logic
input_data = sys.stdin.read()
args = json.loads(input_data)
name = args.get("name", "World")

print(f"Hello, {name}!")
```

The tool will be registered under the name `plugin__hello_tool`. All custom plugins are automatically prefixed with `plugin__`.

## Extensions

Maquis supports custom REPL slash command extensions. When an unknown slash command (e.g. `/stats`) is entered in the REPL, Maquis searches for a matching executable script or binary in the extensions directories.

To list all loaded slash command extensions, type `/extensions` in the REPL.

### Extension Folders

- Global Extensions: `~/.maquis/extensions/`
- Project Extensions: `extensions/` (in the workspace root)

### Writing an Extension

1. Create a script or binary with a filename matching the command (e.g., `stats`, `stats.py`, or `stats.sh` for `/stats`) and make it executable (`chmod +x`). The matching is case-insensitive and ignores the file extension.
2. The script receives any arguments passed after the slash command as standard command-line arguments (`sys.argv` in Python, `$1, $2, ...` in Bash).
3. The complete conversation history is serialized as a JSON array of message objects and passed directly to the script's `stdin`.
4. Any output written by the extension to `stdout` is printed directly to the REPL. A non-zero exit code or stderr output is reported as an extension execution error. Extensions have a 15-second execution timeout.

#### Example Python Extension (extensions/stats.py)

```python
#!/usr/bin/env python3
import sys
import json

# Read and parse conversation history from stdin
try:
    raw_history = sys.stdin.read()
    messages = json.loads(raw_history) if raw_history else []
except Exception as e:
    print(f"Error parsing history: {e}", file=sys.stderr)
    sys.exit(1)

# Retrieve command-line arguments
args = sys.argv[1:]

# Calculate basic statistics
user_msgs = [m for m in messages if m.get("role") == "user"]
assistant_msgs = [m for m in messages if m.get("role") == "assistant"]
total_tokens = sum(m.get("prompt_tokens", 0) + m.get("completion_tokens", 0) for m in messages)

print("--- Conversation Stats ---")
print(f"Total Messages: {len(messages)}")
print(f"User Prompts: {len(user_msgs)}")
print(f"Assistant Responses: {len(assistant_msgs)}")
print(f"Accumulated Tokens: {total_tokens}")

if args:
    print(f"Passed Arguments: {args}")
```

## Multi-Agent Swarm

Maquis includes an asynchronous Multi-Agent Swarm framework designed around Go channels. You can spawn independent agents or hierarchical subagents directly in the REPL.

### Slash Commands

- `/agent`: Opens the interactive Agent Swarm Manager TUI to view, spawn, join, or terminate agents.

### Channel-Based Swarm Communication

Each agent runs asynchronously in its own goroutine and communicates using thread-safe channels (`Input` and `Output`). When a subagent is spawned, the parent agent is equipped with a tool to send a message to the subagent's `Input` channel, block until a response arrives on the subagent's `Output` channel, and incorporate the results back into its own reasoning loop.

## Author

Lucian BLETAN

## License

MIT License
