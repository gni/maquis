package ui

import (
	"bytes"
	"io"
	"testing"

	"golang.org/x/term"

	"maquis/pkg/agent"
	"maquis/pkg/config"
)

func TestNormalizePromptNavigationKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "xterm ctrl left", input: "\x1b[1;5D", want: "\x1b[1;3D"},
		{name: "xterm ctrl right", input: "\x1b[1;5C", want: "\x1b[1;3C"},
		{name: "compact ctrl left", input: "\x1b[5D", want: "\x1b[1;3D"},
		{name: "compact ctrl right", input: "\x1b[5C", want: "\x1b[1;3C"},
		{name: "ss3 ctrl left", input: "\x1bO1;5D", want: "\x1b[1;3D"},
		{name: "ss3 ctrl right", input: "\x1bO1;5C", want: "\x1b[1;3C"},
		{name: "ctrl a passes through", input: "\x01", want: "\x01"},
		{name: "plain arrows pass through", input: "\x1b[D\x1b[C", want: "\x1b[D\x1b[C"},
		{name: "text passes through", input: "one two", want: "one two"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(normalizePromptNavigationKeys([]byte(tt.input))); got != tt.want {
				t.Fatalf("normalizePromptNavigationKeys(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPromptNavigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{
			name: "ctrl left moves to previous word",
			chunks: [][]byte{
				[]byte("alpha beta"),
				[]byte("\x1b[1;5D"),
				[]byte("X"),
				{'\r'},
			},
			want: "alpha Xbeta",
		},
		{
			name: "ctrl right moves to next word",
			chunks: [][]byte{
				[]byte("alpha beta"),
				{'\x01'},
				[]byte("\x1b[1;5C"),
				[]byte("X"),
				{'\r'},
			},
			want: "alpha Xbeta",
		},
		{
			name: "ctrl a moves to beginning",
			chunks: [][]byte{
				[]byte("alpha beta"),
				{'\x01'},
				[]byte("X"),
				{'\r'},
			},
			want: "Xalpha beta",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rw := &chunkReadWriter{chunks: tt.chunks}
			ki := &keyInterceptorReader{
				r:     rw,
				w:     rw,
				agent: &agent.Agent{Config: &config.Config{}},
			}
			rl := term.NewTerminal(ki, "")
			ki.rl = rl

			got, err := rl.ReadLine()
			if err != nil {
				t.Fatalf("ReadLine() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ReadLine() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestPromptNavigationDoesNotRewriteBracketedPaste(t *testing.T) {
	t.Parallel()

	const pastedSequence = "\x1b[1;5D"
	ki := &keyInterceptorReader{
		r:     bytes.NewReader([]byte("\x1b[200~" + pastedSequence + "\x1b[201~")),
		w:     io.Discard,
		agent: &agent.Agent{Config: &config.Config{}},
	}

	buf := make([]byte, 256)
	n, err := ki.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := string(buf[:n]); got != pastedSequence {
		t.Fatalf("Read() = %q; want pasted bytes %q unchanged", got, pastedSequence)
	}
}

type chunkReadWriter struct {
	chunks [][]byte
	output bytes.Buffer
}

func (rw *chunkReadWriter) Read(p []byte) (int, error) {
	if len(rw.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := rw.chunks[0]
	rw.chunks = rw.chunks[1:]
	return copy(p, chunk), nil
}

func (rw *chunkReadWriter) Write(p []byte) (int, error) {
	return rw.output.Write(p)
}
