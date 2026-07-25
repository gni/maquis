package ui

import (
	"strings"
	"testing"
)

func TestContextUsageMarksLiveTokenCountsAsEstimated(t *testing.T) {
	previousUI := ActiveUI
	ActiveUI = &AgentUIImpl{}
	t.Cleanup(func() {
		ActiveUI = previousUI
	})

	UpdateStatus("test", 2100, 900, 900, 128000, true, 0, 0, true)
	live := stripAnsi(formatLeft(UITheme{}, 100))
	if !strings.Contains(live, "~3.0k/128k (~2.3%)") {
		t.Fatalf("live context usage was not marked as estimated: %q", live)
	}

	UpdateStatus("test", 2050, 50, 50, 128000, false, 0, 0, true)
	settled := stripAnsi(formatLeft(UITheme{}, 100))
	if !strings.Contains(settled, "2.1k/128k (1.6%)") {
		t.Fatalf("settled context usage was not rendered exactly: %q", settled)
	}
	if strings.Contains(settled, "~") {
		t.Fatalf("settled provider usage remained marked as estimated: %q", settled)
	}

	UpdateStatus("test", 1650, 0, 0, 128000, false, 0, 0, true, true)
	preflight := stripAnsi(formatLeft(UITheme{}, 100))
	if !strings.Contains(preflight, "~1.6k/128k (~1.3%)") {
		t.Fatalf("idle preflight usage was not marked as estimated: %q", preflight)
	}

	UpdateStatus("test", 1600, 19, 19, 128000, false, 0, 0, true, false)
	measured := stripAnsi(formatLeft(UITheme{}, 100))
	if strings.Contains(measured, "~") {
		t.Fatalf("provider-measured usage remained marked as estimated: %q", measured)
	}
}
