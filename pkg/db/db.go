package db

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var sessionIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

func validateSessionID(sessionID string) error {
	if !sessionIDRegex.MatchString(sessionID) {
		return fmt.Errorf("security violation: invalid session ID format")
	}
	return nil
}

func NewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	PromptTokens     int        `json:"prompt_tokens,omitempty"`
	CompletionTokens int        `json:"completion_tokens,omitempty"`
}

type SessionInfo struct {
	SessionID   string
	Timestamp   string
	MsgCount    int
	Preview     string
	TotalTokens int
}

type JSONLRecord struct {
	Timestamp string  `json:"timestamp"`
	Message   Message `json:"message"`
}

var sessionsDir string

func InitDB(dirPath string) error {
	sessionsDir = filepath.Clean(dirPath)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return fmt.Errorf("failed to create sessions directory: %w", err)
	}
	return nil
}

func SaveMessage(sessionID string, msg Message) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if sessionsDir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	if msg.Role == "user" && msg.Content == "" {
		return nil
	}

	record := JSONLRecord{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Message:   msg,
	}

	jsonData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	filePath := filepath.Join(sessionsDir, sessionID+".jsonl")
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(jsonData, '\n')); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

func LoadMessages(sessionID string) ([]Message, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if sessionsDir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	filePath := filepath.Join(sessionsDir, sessionID+".jsonl")
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var messages []Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record JSONLRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		if record.Message.Role == "user" && record.Message.Content == "" {
			continue
		}

		messages = append(messages, record.Message)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return messages, nil
}

func ClearHistory() error {
	if sessionsDir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read sessions directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			name := strings.TrimSuffix(entry.Name(), ".jsonl")
			if err := validateSessionID(name); err != nil {
				continue
			}
			filePath := filepath.Join(sessionsDir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				return fmt.Errorf("failed to remove session file %s: %w", entry.Name(), err)
			}
		}
	}

	return nil
}

func HasMessages(sessionID string) bool {
	if err := validateSessionID(sessionID); err != nil {
		return false
	}
	if sessionsDir == "" {
		return false
	}
	filePath := filepath.Join(sessionsDir, sessionID+".jsonl")
	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

func ClearSession(sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if sessionsDir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}
	filePath := filepath.Join(sessionsDir, sessionID+".jsonl")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	return nil
}

func GetLatestSessionID() (string, error) {
	if sessionsDir == "" {
		return "", fmt.Errorf("sessions directory not initialized")
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			name := strings.TrimSuffix(entry.Name(), ".jsonl")
			if err := validateSessionID(name); err != nil {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestFile = entry.Name()
			}
		}
	}

	if latestFile == "" {
		return "", nil
	}

	return strings.TrimSuffix(latestFile, ".jsonl"), nil
}

func GetSessions() ([]SessionInfo, error) {
	if sessionsDir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []SessionInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		if err := validateSessionID(sessionID); err != nil {
			continue
		}
		filePath := filepath.Join(sessionsDir, entry.Name())

		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		var firstTimestamp string
		var msgCount int
		var totalTokens int
		var previewParts []string

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var record JSONLRecord
			if err := json.Unmarshal(line, &record); err != nil {
				continue
			}

			if record.Message.Role == "user" && record.Message.Content == "" {
				continue
			}

			msgCount++
			if firstTimestamp == "" {
				firstTimestamp = record.Timestamp
			}

			totalTokens += record.Message.PromptTokens + record.Message.CompletionTokens

			if record.Message.Role != "system" && record.Message.Content != "" {
				previewParts = append(previewParts, record.Message.Content)
			}
		}
		f.Close()

		if msgCount == 0 {
			continue
		}

		if firstTimestamp == "" {
			info, err := entry.Info()
			if err == nil {
				firstTimestamp = info.ModTime().Format("2006-01-02 15:04:05")
			} else {
				firstTimestamp = time.Now().Format("2006-01-02 15:04:05")
			}
		}

		var preview string
		if len(previewParts) > 0 {
			cleanText := strings.TrimSpace(strings.Join(previewParts, " "))
			cleanText = strings.Join(strings.Fields(cleanText), " ")
			runes := []rune(cleanText)
			if len(runes) > 60 {
				preview = string(runes[:60]) + "..."
			} else {
				preview = cleanText
			}
		} else {
			preview = "(No text content)"
		}

		sessions = append(sessions, SessionInfo{
			SessionID:   sessionID,
			Timestamp:   firstTimestamp,
			MsgCount:    msgCount,
			Preview:     preview,
			TotalTokens: totalTokens,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Timestamp > sessions[j].Timestamp
	})

	return sessions, nil
}

type sessionWithStart struct {
	filePath  string
	startTime string
}

func GetUserHistory() ([]string, error) {
	if sessionsDir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []sessionWithStart

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		if err := validateSessionID(sessionID); err != nil {
			continue
		}

		filePath := filepath.Join(sessionsDir, entry.Name())
		startTime := ""

		f, err := os.Open(filePath)
		if err == nil {
			scanner := bufio.NewScanner(f)
			if scanner.Scan() {
				lineBytes := scanner.Bytes()
				if len(lineBytes) >= 33 && bytes.HasPrefix(lineBytes, []byte(`{"timestamp":"`)) {
					startTime = string(lineBytes[14:33])
				} else {
					var record JSONLRecord
					if json.Unmarshal(lineBytes, &record) == nil {
						startTime = record.Timestamp
					}
				}
			}
			f.Close()
		}

		if startTime == "" {
			info, err := entry.Info()
			if err == nil {
				startTime = info.ModTime().Format("2006-01-02 15:04:05")
			} else {
				startTime = time.Now().Format("2006-01-02 15:04:05")
			}
		}

		files = append(files, sessionWithStart{
			filePath:  filePath,
			startTime: startTime,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].startTime < files[j].startTime
	})

	var history []string
	var lastContent string

	for _, item := range files {
		f, err := os.Open(item.filePath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lineBytes := scanner.Bytes()
			if !bytes.Contains(lineBytes, []byte(`"role":"user"`)) {
				continue
			}

			var record JSONLRecord
			if err := json.Unmarshal(lineBytes, &record); err != nil {
				continue
			}

			msg := record.Message
			if msg.Role == "user" && !strings.HasPrefix(strings.ToLower(msg.Content), "[user manually executed") {
				content := strings.TrimSpace(msg.Content)
				if content != "" && content != lastContent {
					history = append(history, content)
					lastContent = content
				}
			}
		}
		f.Close()
	}

	return history, nil
}