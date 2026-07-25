package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultTurnActivityInterval = 100 * time.Millisecond

var turnActivityFrames = [...]string{"◜", "◝", "◞", "◟"}

type turnActivityPhase uint8

const (
	turnActivityThinking turnActivityPhase = iota
	turnActivityExecuting
	turnActivityPaused
)

type turnActivityCommand struct {
	phase    turnActivityPhase
	toolName string
	stop     bool
	ack      chan struct{}
}

// turnActivity is the sole owner of the transient activity row for one agent
// turn. Thinking and tool execution share the same monotonic start time, so a
// phase change cannot introduce a second writer or reset the elapsed counter.
type turnActivity struct {
	commands chan turnActivityCommand
	done     chan struct{}
	stopOnce sync.Once
}

func newTurnActivity(startedAt time.Time, interval time.Duration, draw func(frame, text string)) *turnActivity {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if interval <= 0 {
		interval = defaultTurnActivityInterval
	}

	activity := &turnActivity{
		commands: make(chan turnActivityCommand),
		done:     make(chan struct{}),
	}
	go activity.run(startedAt, interval, draw)
	return activity
}

func (a *turnActivity) Think() {
	a.setPhase(turnActivityThinking, "")
}

func (a *turnActivity) Execute(toolName string) {
	a.setPhase(turnActivityExecuting, sanitizeActivityToolName(toolName))
}

func (a *turnActivity) Pause() {
	a.setPhase(turnActivityPaused, "")
}

func (a *turnActivity) Stop() {
	if a == nil {
		return
	}

	a.stopOnce.Do(func() {
		ack := make(chan struct{})
		command := turnActivityCommand{stop: true, ack: ack}
		select {
		case a.commands <- command:
			select {
			case <-ack:
			case <-a.done:
			}
		case <-a.done:
		}
		<-a.done
	})
}

func (a *turnActivity) setPhase(phase turnActivityPhase, toolName string) {
	if a == nil {
		return
	}

	ack := make(chan struct{})
	command := turnActivityCommand{
		phase:    phase,
		toolName: toolName,
		ack:      ack,
	}
	select {
	case a.commands <- command:
		select {
		case <-ack:
		case <-a.done:
		}
	case <-a.done:
	}
}

func (a *turnActivity) run(startedAt time.Time, interval time.Duration, draw func(frame, text string)) {
	defer close(a.done)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	phase := turnActivityThinking
	toolName := ""
	frameIndex := 0
	var lastElapsed time.Duration

	render := func(now time.Time) {
		if draw == nil || phase == turnActivityPaused {
			return
		}

		elapsed := now.Sub(startedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed < lastElapsed {
			elapsed = lastElapsed
		} else {
			lastElapsed = elapsed
		}

		frame := turnActivityFrames[frameIndex%len(turnActivityFrames)]
		frameIndex++
		draw(frame, formatTurnActivityText(phase, toolName, elapsed))
	}

	render(time.Now())

	for {
		select {
		case now := <-ticker.C:
			render(now)
		case command := <-a.commands:
			if command.stop {
				if phase != turnActivityPaused && draw != nil {
					draw("", "")
				}
				close(command.ack)
				return
			}

			changed := phase != command.phase || toolName != command.toolName
			phase = command.phase
			toolName = command.toolName

			if changed {
				if phase == turnActivityPaused {
					if draw != nil {
						draw("", "")
					}
				} else {
					render(time.Now())
				}
			}
			close(command.ack)
		}
	}
}

func formatTurnActivityText(phase turnActivityPhase, toolName string, elapsed time.Duration) string {
	elapsedText := fmt.Sprintf("(%.1fs)", elapsed.Seconds())
	if phase != turnActivityExecuting {
		return elapsedText
	}
	return fmt.Sprintf("executing %s... %s", toolName, elapsedText)
}

func sanitizeActivityToolName(toolName string) string {
	toolName = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, toolName)
	toolName = strings.Join(strings.Fields(toolName), " ")
	if toolName == "" {
		return "tool"
	}
	return toolName
}
