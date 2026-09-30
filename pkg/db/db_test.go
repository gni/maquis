package db

import (
	"os"
	"testing"
)

func TestValidateSessionID(t *testing.T) {
	validIDs := []string{"session-1", "abc_123", "UUID-999000-111"}
	for _, id := range validIDs {
		if err := validateSessionID(id); err != nil {
			t.Errorf("expected valid session ID for %q, got error: %v", id, err)
		}
	}

	invalidIDs := []string{"session/1", "abc;drop table", "../secret", "hello world"}
	for _, id := range invalidIDs {
		if err := validateSessionID(id); err == nil {
			t.Errorf("expected error for invalid session ID %q, got nil", id)
		}
	}
}

func TestNewUUID(t *testing.T) {
	u1 := NewUUID()
	u2 := NewUUID()
	if len(u1) == 0 || len(u2) == 0 {
		t.Fatalf("expected non-empty UUIDs")
	}
	if u1 == u2 {
		t.Errorf("expected unique UUIDs, got duplicates: %s", u1)
	}
}

func TestDBLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "maquis_db_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := InitDB(tempDir); err != nil {
		t.Fatalf("failed to init DB: %v", err)
	}

	sessionID := "test-session-001"
	msg1 := Message{Role: "user", Content: "Hello maquis"}
	if err := SaveMessage(sessionID, msg1); err != nil {
		t.Fatalf("failed to save message: %v", err)
	}

	msg2 := Message{Role: "assistant", Content: "Hello! I am ready."}
	if err := SaveMessage(sessionID, msg2); err != nil {
		t.Fatalf("failed to save assistant message: %v", err)
	}

	if !HasMessages(sessionID) {
		t.Fatalf("expected HasMessages to return true for %s", sessionID)
	}

	messages, err := LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Content != "Hello maquis" || messages[1].Content != "Hello! I am ready." {
		t.Errorf("unexpected message contents: %+v", messages)
	}

	sessions, err := GetSessions()
	if err != nil {
		t.Fatalf("failed to get sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session info, got %d", len(sessions))
	}
	if sessions[0].SessionID != sessionID {
		t.Errorf("expected sessionID %s, got %s", sessionID, sessions[0].SessionID)
	}

	if err := ClearSession(sessionID); err != nil {
		t.Fatalf("failed to clear session: %v", err)
	}
	if HasMessages(sessionID) {
		t.Fatalf("expected session to be cleared")
	}
}

func TestRewriteSession(t *testing.T) {
	tempDir := t.TempDir()
	if err := InitDB(tempDir); err != nil {
		t.Fatalf("failed to init DB: %v", err)
	}

	sessionID := "test-rewrite-001"
	origMsgs := []Message{
		{Role: "user", Content: "initial user message"},
		{Role: "assistant", Content: "initial assistant response"},
	}
	for _, m := range origMsgs {
		if err := SaveMessage(sessionID, m); err != nil {
			t.Fatalf("failed to save initial msg: %v", err)
		}
	}

	newMsgs := []Message{
		{Role: "system", Content: "system summary prompt"},
		{Role: "user", Content: "latest question"},
		{Role: "assistant", Content: "latest answer"},
	}

	if err := RewriteSession(sessionID, newMsgs); err != nil {
		t.Fatalf("RewriteSession failed: %v", err)
	}

	loaded, err := LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("failed to load rewritten messages: %v", err)
	}

	if len(loaded) != len(newMsgs) {
		t.Fatalf("expected %d messages, got %d", len(newMsgs), len(loaded))
	}

	for i, m := range loaded {
		if m.Role != newMsgs[i].Role || m.Content != newMsgs[i].Content {
			t.Errorf("mismatch at %d: got %+v, want %+v", i, m, newMsgs[i])
		}
	}
}

func TestLatestSessionTracking(t *testing.T) {
	tempDir := t.TempDir()
	if err := InitDB(tempDir); err != nil {
		t.Fatalf("failed to init DB: %v", err)
	}

	session1 := "session-alpha-111"
	session2 := "session-beta-222"

	// Save message in session 1
	if err := SaveMessage(session1, Message{Role: "user", Content: "hello alpha"}); err != nil {
		t.Fatalf("failed to save msg1: %v", err)
	}

	latest, err := GetLatestSessionID()
	if err != nil || latest != session1 {
		t.Fatalf("expected latest session to be %s, got %s, err: %v", session1, latest, err)
	}

	// Save message in session 2
	if err := SaveMessage(session2, Message{Role: "user", Content: "hello beta"}); err != nil {
		t.Fatalf("failed to save msg2: %v", err)
	}

	latest, err = GetLatestSessionID()
	if err != nil || latest != session2 {
		t.Fatalf("expected latest session to be %s, got %s, err: %v", session2, latest, err)
	}

	// Explicitly switch/use session 1
	if err := SetLatestSessionID(session1); err != nil {
		t.Fatalf("SetLatestSessionID failed: %v", err)
	}

	latest, err = GetLatestSessionID()
	if err != nil || latest != session1 {
		t.Fatalf("expected latest session to be %s after switch, got %s", session1, latest)
	}

	// Delete session 1
	if err := ClearSession(session1); err != nil {
		t.Fatalf("ClearSession failed: %v", err)
	}

	// Should fall back to session 2
	latest, err = GetLatestSessionID()
	if err != nil || latest != session2 {
		t.Fatalf("expected fallback to %s after clearing session 1, got %s", session2, latest)
	}
}
