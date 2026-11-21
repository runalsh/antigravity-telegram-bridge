package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// ChatState is the per-chat binding: which Antigravity conversation this
// Telegram chat is currently driving.
type ChatState struct {
	ConvID  string   `json:"conv_id"`
	Title   string   `json:"title"`
	TLMsg   int64    `json:"tl_msg"` // Telegram message id being streamed/edited
	Cwd     string   `json:"cwd"`
	Mirror  bool     `json:"mirror"`  // true = read-only transcript tailing
	Pending []string `json:"pending"` // options of the last unanswered ask_question
}

type State struct {
	mu       sync.Mutex
	path     string
	UpdateOK int64                `json:"update_offset"`
	Chats    map[int64]*ChatState `json:"chats"`
	ReadPos  map[string]int64     `json:"read_pos"` // transcript path -> byte offset
}

func newState(path string) *State {
	s := &State{
		path:    path,
		Chats:   map[int64]*ChatState{},
		ReadPos: map[string]int64{},
	}
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, s); err != nil {
			log.Printf("state: cannot parse %s: %v (starting fresh)", path, err)
		}
	}
	if s.Chats == nil {
		s.Chats = map[int64]*ChatState{}
	}
	if s.ReadPos == nil {
		s.ReadPos = map[string]int64{}
	}
	return s
}

func (s *State) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

func (s *State) saveLocked() {
	dir := filepath.Dir(s.path)
	_ = os.MkdirAll(dir, 0o755)
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("state: write: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("state: rename: %v", err)
	}
}

func (s *State) chat(chatID int64) *ChatState {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.Chats[chatID]
	if !ok {
		cs = &ChatState{}
		s.Chats[chatID] = cs
		s.saveLocked()
	}
	return cs
}

func (s *State) setChat(chatID int64, cs ChatState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cs
	s.Chats[chatID] = &cp
	s.saveLocked()
}

func (s *State) forget(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Chats, chatID)
	s.saveLocked()
}

func (s *State) offset(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ReadPos[key]
}

func (s *State) setOffset(key string, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReadPos[key] = v
}

func (s *State) setOffsetPersist(key string, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReadPos[key] = v
	s.saveLocked()
}
