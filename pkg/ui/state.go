package ui

import (
	"io"
	"sync"

	"maquis/pkg/ui/style"
)

// ActiveUI holds a global reference to the currently active TUI instance.
var ActiveUI *AgentUIImpl

// fallbackUI is a local instance used if ActiveUI is nil (e.g., during unit tests).
var fallbackUI = &AgentUIImpl{}

func getUI() *AgentUIImpl {
	if ActiveUI != nil {
		return ActiveUI
	}
	return fallbackUI
}

// TerminalMu protects terminal output operations.
var TerminalMu sync.Mutex

// IsInteractive indicates if the interactive REPL session is currently running.
var IsInteractive bool

// CancelActiveOperation safely cancels the currently running agent turn.
func CancelActiveOperation() bool {
	return getUI().CancelActiveOperation()
}

// SetCollapseStatus updates the results collapsing state in the status bar.
func SetCollapseStatus(collapsed bool) {
	getUI().SetCollapseStatus(collapsed)
}

// SetScrollRegionOffset reserves extra lines above the status bar's normal 2-line area.
func SetScrollRegionOffset(offset int) {
	getUI().SetScrollRegionOffset(offset)
}


// InitStatusBar starts the status bar.
func InitStatusBar(w io.Writer) {
	getUI().InitStatusBar(w)
}

// ShutdownStatusBar cleans up the status bar.
func ShutdownStatusBar(w io.Writer) {
	getUI().ShutdownStatusBar(w)
}

func stripAnsi(str string) string {
	return style.StripAnsi(str)
}
