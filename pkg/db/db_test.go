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
