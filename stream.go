package main

import (
	"encoding/json"
	"path/filepath"
)

// streamEvent is one NDJSON record from `agy --output-format stream-json`.
// Verified shape: init / step_update / result.
type streamEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Init           struct {
		Cwd            string `json:"cwd"`
		PermissionMode string `json:"permission_mode"`
	} `json:"init"`
	StepUpdate struct {
		ConversationID string `json:"conversation_id"`
		StepIndex      int    `json:"step_index"`
		State          string `json:"state"`
		StepType       string `json:"step_type"`
		ToolName       string `json:"tool_name"`
		TextDelta      string `json:"text_delta"`
		ToolInfo       struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
			Output     string         `json:"output"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
		NumTurns       int    `json:"num_turns"`
		Usage          struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

// userMessage renders the stdin record agy expects on --input-format stream-json.
// Note the field is "event", not "type" (verified against the CLI).
func userMessage(text string) string {
	raw, _ := json.Marshal(map[string]any{
		"event": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	return string(raw) + "\n"
}

// conversationTranscript maps a conversation id to its transcript log file.
// This file is written by the hub for chats driven inside VS Code too, which is
// what makes read-only mirroring of IDE chats possible.
func conversationTranscript(geminiDir, convID string) string {
	return filepath.Join(geminiDir, "brain", convID, ".system_generated", "logs", "transcript.jsonl")
}
