package ui

import (
	"context"
	"io"
	"sync"

	"maquis/pkg/agent"
)

// ActiveCancelFunc holds the cancel function for the currently running agent loop.
var ActiveCancelFunc context.CancelFunc

// TerminalMu protects terminal output operations.
var TerminalMu = &agent.TerminalMu

// pasteLinesOffset tracks the number of lines shifted by a paste operation.
var pasteLinesOffset int

// activeInputReader keeps track of the currently active keyInterceptorReader.
var activeInputReader io.Reader

// inApprovalPrompt is true when the UI is waiting for user approval.
var inApprovalPrompt bool

// state holds the current state metrics for the status bar.
var state StatusBarState

// stateMu synchronizes access to status bar state and layout variables.
var stateMu sync.Mutex

// lastH tracks the last recorded terminal height.
var lastH int

// enabled flags whether the status bar is active.
var enabled bool

// scrollRegionOffset defines the extra lines reserved above the status bar.
var scrollRegionOffset int

// collapseResults flags whether tool outputs should be collapsed.
var collapseResults bool

// lastStatsText buffers the last drawn stats line content.
var lastStatsText string

// IsInteractive indicates if the interactive REPL session is currently running.
var IsInteractive bool

// CancelActiveOperation safely cancels the currently running agent turn.
func CancelActiveOperation() bool {
	stateMu.Lock()
	cancel := ActiveCancelFunc
	stateMu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}
