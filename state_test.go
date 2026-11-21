package main

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestStateLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	st := newState(statePath)
	if st == nil {
		t.Fatal("newState returned nil")
	}

	cs := st.chat(1001)
	if cs == nil {
		t.Fatal("chat returned nil ChatState")
	}
	if cs.ConvID != "" {
		t.Errorf("expected empty ConvID for new chat, got %s", cs.ConvID)
	}

	cs.ConvID = "conv-abc-123"
	cs.Title = "test conversation"
	cs.Mirror = true
	cs.Pending = []string{"Option 1", "Option 2"}
	st.setChat(1001, *cs)

	// Reopen state from disk
	st2 := newState(statePath)
	cs2 := st2.chat(1001)
	if cs2.ConvID != "conv-abc-123" {
		t.Errorf("expected conv-abc-123, got %s", cs2.ConvID)
	}
	if !cs2.Mirror {
		t.Errorf("expected Mirror to be true")
	}
	if len(cs2.Pending) != 2 || cs2.Pending[0] != "Option 1" {
		t.Errorf("unexpected Pending options: %#v", cs2.Pending)
	}
}

func TestStateForget(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	st := newState(statePath)
	cs := ChatState{ConvID: "to-delete"}
	st.setChat(2002, cs)

	st.forget(2002)

	st2 := newState(statePath)
	if _, exists := st2.Chats[2002]; exists {
		t.Errorf("expected chat 2002 to be forgotten")
	}
}

func TestStateConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state_concurrent.json")
	st := newState(statePath)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		chatID := int64(1000 + i)
		go func(id int64) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cs := st.chat(id)
				cs.ConvID = "conv-loop"
				st.setChat(id, *cs)
				st.Save()
			}
		}(chatID)
	}
	wg.Wait()

	if len(st.Chats) != 20 {
		t.Errorf("expected 20 chats, got %d", len(st.Chats))
	}
}
