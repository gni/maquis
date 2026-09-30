package ui

import (
	"fmt"
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

var (
	alternateScreenDepth int
	alternateScreenMu    sync.Mutex
)

// EnterAlternateScreen switches the terminal to the alternate screen buffer.
// It is reference-counted so nested full-screen components (like sub-menus)
// don't prematurely exit the alternate screen.
func EnterAlternateScreen(w io.Writer) {
	alternateScreenMu.Lock()
	defer alternateScreenMu.Unlock()
	if alternateScreenDepth == 0 {
		fmt.Fprint(w, "\x1b[?1049h\x1b[r\x1b[2J\x1b[H")
	}
	alternateScreenDepth++
}

// ExitAlternateScreen decrements the alternate screen reference count and
// restores the primary screen buffer when the count reaches zero.
func ExitAlternateScreen(w io.Writer) {
	alternateScreenMu.Lock()
	defer alternateScreenMu.Unlock()
	if alternateScreenDepth > 0 {
		alternateScreenDepth--
		if alternateScreenDepth == 0 {
			fmt.Fprint(w, "\x1b[?1049l\x1b[?25h")
		}
	}
}

// ForceExitAlternateScreen unconditionally exits the alternate screen buffer
// and restores the primary terminal buffer and visible cursor.
func ForceExitAlternateScreen(w io.Writer) {
	alternateScreenMu.Lock()
	defer alternateScreenMu.Unlock()
	if alternateScreenDepth > 0 {
		alternateScreenDepth = 0
		fmt.Fprint(w, "\x1b[?1049l\x1b[?25h")
	}
}
