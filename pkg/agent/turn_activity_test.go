package agent

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type activitySample struct {
	frame string
	text  string
}

var activityElapsedPattern = regexp.MustCompile(`\(([0-9]+\.[0-9])s\)$`)

func waitForActivityPhase(t *testing.T, samples <-chan activitySample, executing bool) activitySample {
	t.Helper()

	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()

	for {
		select {
		case sample := <-samples:
			if strings.HasPrefix(sample.text, "executing ") == executing {
				return sample
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for executing=%t activity sample", executing)
		}
	}
}

func activityElapsedSeconds(t *testing.T, sample activitySample) float64 {
	t.Helper()

	match := activityElapsedPattern.FindStringSubmatch(sample.text)
	if len(match) != 2 {
		t.Fatalf("activity sample has no elapsed counter: %q", sample.text)
	}
	elapsed, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("parse elapsed counter %q: %v", match[1], err)
	}
	return elapsed
}

func TestTurnActivityUsesOneMonotonicCounterAcrossToolExecution(t *testing.T) {
	samples := make(chan activitySample, 128)
	startedAt := time.Now().Add(-1200 * time.Millisecond)
	activity := newTurnActivity(startedAt, 5*time.Millisecond, func(frame, text string) {
		samples <- activitySample{frame: frame, text: text}
	})
	t.Cleanup(activity.Stop)

	thinking := waitForActivityPhase(t, samples, false)

	activity.Execute("bash")
	executing := waitForActivityPhase(t, samples, true)
	if !strings.HasPrefix(executing.text, "executing bash... ") {
		t.Fatalf("unexpected execution status: %q", executing.text)
	}

	for range 3 {
		sample := waitForActivityPhase(t, samples, true)
		if !strings.HasPrefix(sample.text, "executing bash... ") {
			t.Fatalf("thinking status leaked into tool execution: %q", sample.text)
		}
	}

	activity.Think()
	resumed := waitForActivityPhase(t, samples, false)

	thinkingElapsed := activityElapsedSeconds(t, thinking)
	executingElapsed := activityElapsedSeconds(t, executing)
	resumedElapsed := activityElapsedSeconds(t, resumed)
	if executingElapsed < thinkingElapsed {
		t.Fatalf("tool counter reset from %.1fs to %.1fs", thinkingElapsed, executingElapsed)
	}
	if resumedElapsed < executingElapsed {
		t.Fatalf("resumed counter reset from %.1fs to %.1fs", executingElapsed, resumedElapsed)
	}
	if executingElapsed < 1 {
		t.Fatalf("tool counter used a new start time: %.1fs", executingElapsed)
	}
}
