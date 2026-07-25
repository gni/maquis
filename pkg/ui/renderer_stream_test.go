package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"maquis/pkg/ui/style"
)

var sgrSequencePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestResponseStreamingIsAppendOnly(t *testing.T) {
	useIsolatedCursorTestUI(t)

	const line = "The user is asking to list agents and inspect the available swarm tools."

	var terminal bytes.Buffer
	writer := NewPromptPreservingWriter(&terminal, 30)
	renderer := NewStreamRenderer(writer, UITheme{}, false, false, "test")

	for _, chunk := range []string{
		"The user is asking to ",
		"list agents and inspect ",
		"the available swarm tools.",
	} {
		renderer.Write(chunk)
	}
	renderer.Write("\n")
	renderer.Flush()

	rendered := sanitizeTerminalText(terminal.String())
	if count := strings.Count(rendered, line); count != 1 {
		t.Fatalf("streamed response was written %d times; want exactly once: %q", count, rendered)
	}
	if strings.Contains(terminal.String(), "\x1b[2K") {
		t.Fatalf("streamed response cleared and replayed an existing row: %q", terminal.String())
	}
}

func TestReasoningStreamingIsAppendOnly(t *testing.T) {
	useIsolatedCursorTestUI(t)

	const reasoning = "The user is asking to \"list agents\". Looking at the available tools, there isn't a direct `list_agents` tool, " +
		"but there is `swarm_topology` which shows the tree\n" +
		"hierarchy of subagents. I will use `swarm_topology` to see if there are any active subagents."

	var terminal bytes.Buffer
	writer := NewPromptPreservingWriter(&terminal, 30)
	renderer := NewStreamRenderer(writer, UITheme{}, true, false, "test")

	for _, chunk := range []string{
		"The user is asking to \"list agents\". ",
		"Looking at the available tools, there isn't a direct `list_agents` tool, ",
		"but there is `swarm_topology` which shows the tree\n",
		"hierarchy of subagents. I will use `swarm_topology` ",
		"to see if there are any active subagents.",
	} {
		renderer.WriteReasoning(chunk)
	}
	renderer.WriteReasoning("\n")
	renderer.EndThinking()

	rendered := sanitizeTerminalText(terminal.String())
	if count := strings.Count(rendered, reasoning); count != 1 {
		t.Fatalf("streamed reasoning was written %d times; want exactly once: %q", count, rendered)
	}
	if strings.Contains(terminal.String(), "\x1b[2K") {
		t.Fatalf("streamed reasoning cleared and replayed an existing row: %q", terminal.String())
	}
}

func TestStreamedFencedCodeIsHighlightedOnce(t *testing.T) {
	useIsolatedCursorTestUI(t)

	const code = "# ssl_checker/app/models/certificate.py\n" +
		"from pydantic import BaseModel, Field\n" +
		"class HostnameCheckRequest(BaseModel):\n" +
		"    hostname: str = Field(..., description=\"The hostname to check\")"

	var terminal bytes.Buffer
	writer := NewPromptPreservingWriter(&terminal, 30)
	renderer := NewStreamRenderer(
		writer,
		UITheme{ChromaStyle: "dracula"},
		false,
		false,
		"test",
	)

	for _, chunk := range []string{
		"```py",
		"thon\n# ssl_checker/app/models/certificate.py\nfrom pydantic ",
		"import BaseModel, Field\nclass HostnameCheckRequest(BaseModel):\n",
		"    hostname: str = Field(..., description=\"The hostname to check\")\n``",
		"`\n",
	} {
		renderer.Write(chunk)
	}
	renderer.Flush()

	raw := terminal.String()
	rendered := sanitizeTerminalText(raw)
	if rendered != code {
		t.Fatalf("streamed code changed or was replayed:\ngot:  %q\nwant: %q", rendered, code)
	}
	if strings.Contains(raw, "```") {
		t.Fatalf("streamed code retained Markdown fences: %q", raw)
	}
	if strings.Contains(raw, "\x1b[2K") {
		t.Fatalf("streamed code cleared an existing row: %q", raw)
	}
	if !sgrSequencePattern.MatchString(raw) {
		t.Fatalf("streamed code contained no syntax-color sequences: %q", raw)
	}
}

func TestMarkdownStylesAreAppliedBeforeStreamCompletion(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var terminal bytes.Buffer
	writer := NewPromptPreservingWriter(&terminal, 30)
	renderer := NewStreamRenderer(
		writer,
		UITheme{
			Primary:   style.Color("#88c0d0"),
			Secondary: style.Color("#b48ead"),
			Border:    style.Color("#4c566a"),
			Highlight: style.Color("#ebcb8b"),
		},
		false,
		false,
		"test",
	)

	for _, chunk := range []string{"# Realtime ", "*", "*bo"} {
		renderer.Write(chunk)
	}
	if partial := sanitizeTerminalText(terminal.String()); partial != "Realtime bo" {
		t.Fatalf("Markdown was not rendered during streaming: %q", partial)
	}

	for _, chunk := range []string{
		"ld** and *ita",
		"lic* with `co",
		"de`\n",
		"- item\n",
		"> quoted\n",
		"1. numbered\n",
	} {
		renderer.Write(chunk)
	}
	renderer.Flush()

	raw := terminal.String()
	rendered := sanitizeTerminalText(raw)
	const expected = "Realtime bold and italic with code\n  • item\n┃ quoted\n  1. numbered"
	if rendered != expected {
		t.Fatalf("realtime Markdown output:\ngot:  %q\nwant: %q", rendered, expected)
	}
	for _, marker := range []string{"# ", "**", "*italic*", "`code`"} {
		if strings.Contains(rendered, marker) {
			t.Fatalf("realtime Markdown retained marker %q in %q", marker, rendered)
		}
	}
	if !sgrSequencePattern.MatchString(raw) {
		t.Fatalf("realtime Markdown contained no style sequences: %q", raw)
	}
	if strings.Contains(raw, "\x1b[2K") {
		t.Fatalf("realtime Markdown cleared an existing row: %q", raw)
	}
}
