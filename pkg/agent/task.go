package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"
)

type Task struct {
	ID             string
	Command        string
	Status         string
	Stdout         *bytes.Buffer
	Stderr         *bytes.Buffer
	Cmd            *exec.Cmd
	StartTime      time.Time
	EndTime        time.Time
	Err            error
	mu             sync.Mutex
	LastOutputTime time.Time
}

type TaskInfo struct {
	ID        string
	Command   string
	Status    string
	Duration  time.Duration
	BytesOut  int
}

type taskWriter struct {
	task     *Task
	agent    *Agent
	w        io.Writer
	isStderr bool
}

func (tw *taskWriter) Write(p []byte) (n int, err error) {
	tw.task.mu.Lock()

	// Keep buffer capped at ~100KB to prevent OOM on long tasks
	capBuf := func(buf *bytes.Buffer) {
		const maxLen = 100 * 1024
		if buf.Len() > maxLen {
			buf.Next(buf.Len() - maxLen)
		}
	}

	if tw.isStderr {
		tw.task.Stderr.Write(p)
		capBuf(tw.task.Stderr)
	} else {
		tw.task.Stdout.Write(p)
		capBuf(tw.task.Stdout)
	}
	tw.task.LastOutputTime = time.Now()
	tw.task.mu.Unlock()

	tw.agent.TasksMu.Lock()
	isStreaming := tw.agent.StreamingTask == tw.task.ID
	tw.agent.TasksMu.Unlock()

	if isStreaming {
		return tw.w.Write(p)
	}

	return len(p), nil
}

func (a *Agent) SpawnTask(command string, w io.Writer) (string, error) {
	a.TasksMu.Lock()
	id := fmt.Sprintf("task_%d", a.NextTaskId)
	a.NextTaskId++

	task := &Task{
		ID:             id,
		Command:        command,
		Status:         "running",
		Stdout:         new(bytes.Buffer),
		Stderr:         new(bytes.Buffer),
		StartTime:      time.Now(),
		LastOutputTime: time.Now(),
	}
	a.Tasks[id] = task
	a.TasksMu.Unlock()

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	task.Cmd = cmd

	stdoutWriter := &taskWriter{task: task, agent: a, w: w, isStderr: false}
	stderrWriter := &taskWriter{task: task, agent: a, w: w, isStderr: true}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	err := cmd.Start()
	if err != nil {
		task.mu.Lock()
		task.Status = "failed"
		task.EndTime = time.Now()
		task.Err = err
		task.mu.Unlock()
		return id, err
	}

	go func() {
		waitErr := cmd.Wait()
		task.mu.Lock()
		task.EndTime = time.Now()
		if task.Status == "running" {
			if waitErr != nil {
				task.Status = "failed"
				task.Err = waitErr
			} else {
				task.Status = "completed"
			}
		}
		finalStatus := task.Status
		task.mu.Unlock()

		a.TasksMu.Lock()
		if a.StreamingTask == task.ID {
			a.StreamingTask = ""
			fmt.Fprintf(w, "\n[Task %s finished with status: %s]\n", task.ID, finalStatus)
		}
		a.TasksMu.Unlock()
		
		// Send event to the agent loop!
		select {
		case a.SystemEvents <- fmt.Sprintf("System Event: Background task %s finished with status %s. Please review the output or logs.", task.ID, finalStatus):
		default:
		}
	}()

	return id, nil
}

func (a *Agent) KillTask(id string) error {
	a.TasksMu.Lock()
	task, exists := a.Tasks[id]
	a.TasksMu.Unlock()

	if !exists {
		return fmt.Errorf("task not found")
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if task.Status != "running" {
		return fmt.Errorf("task is not running (status: %s)", task.Status)
	}

	if task.Cmd != nil && task.Cmd.Process != nil {
		pgid, err := syscall.Getpgid(task.Cmd.Process.Pid)
		if err == nil {
			err = syscall.Kill(-pgid, syscall.SIGKILL)
			if err != nil {
				_ = task.Cmd.Process.Kill()
			}
		} else {
			err = task.Cmd.Process.Kill()
			if err != nil {
				return err
			}
		}
	}

	task.Status = "killed"
	task.EndTime = time.Now()
	return nil
}

func (a *Agent) GetTaskStatus(id string) (string, string, error) {
	a.TasksMu.Lock()
	task, exists := a.Tasks[id]
	a.TasksMu.Unlock()

	if !exists {
		return "", "", fmt.Errorf("task not found")
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	var sb bytes.Buffer
	
	// Helper function to get the tail of a byte slice
	getTail := func(b []byte, maxLen int) []byte {
		if len(b) > maxLen {
			return append([]byte(fmt.Sprintf("...(truncated %d bytes)...\n", len(b)-maxLen)), b[len(b)-maxLen:]...)
		}
		return b
	}

	if task.Stdout.Len() > 0 {
		// sb.WriteString("STDOUT:\n")
		sb.Write(getTail(task.Stdout.Bytes(), 4000))
		sb.WriteString("\n")
	}
	if task.Stderr.Len() > 0 {
		// sb.WriteString("STDERR:\n")
		sb.Write(getTail(task.Stderr.Bytes(), 4000))
		sb.WriteString("\n")
	}

	output := sb.String()
	if output == "" {
		output = "(no output)"
	}

	return task.Status, output, nil
}

func (a *Agent) ListTasks() []TaskInfo {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	var list []TaskInfo
	for _, t := range a.Tasks {
		t.mu.Lock()
		status := t.Status
		cmd := t.Command
		start := t.StartTime
		end := t.EndTime
		if end.IsZero() {
			end = time.Now()
		}
		bytesCount := t.Stdout.Len() + t.Stderr.Len()
		t.mu.Unlock()

		list = append(list, TaskInfo{
			ID:       t.ID,
			Command:  cmd,
			Status:   status,
			Duration: end.Sub(start),
			BytesOut: bytesCount,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})

	return list
}

func (a *Agent) ToggleStreaming(id string, w io.Writer) {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	if a.StreamingTask == id {
		a.StreamingTask = ""
		fmt.Fprintf(w, "\n[Stopped streaming output of %s]\n", id)
		return
	}

	task, exists := a.Tasks[id]
	if !exists {
		fmt.Fprintf(w, "\nError: task %s not found\n", id)
		return
	}

	a.StreamingTask = id
	fmt.Fprintf(w, "\n[Streaming output of %s...]\n", id)

	task.mu.Lock()
	outBytes := task.Stdout.Bytes()
	errBytes := task.Stderr.Bytes()
	task.mu.Unlock()

	if len(outBytes) > 0 {
		_, _ = w.Write(outBytes)
	}
	if len(errBytes) > 0 {
		_, _ = w.Write(errBytes)
	}
}

func (a *Agent) GetLastRunningTaskId() string {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	var lastRunningId string
	var lastTime time.Time
	for _, t := range a.Tasks {
		t.mu.Lock()
		isRunning := t.Status == "running"
		startTime := t.StartTime
		t.mu.Unlock()

		if isRunning && (lastRunningId == "" || startTime.After(lastTime)) {
			lastRunningId = t.ID
			lastTime = startTime
		}
	}
	return lastRunningId
}