package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AgentAPI wraps `agy agentapi ...`, the supported way to push text into a
// conversation the VS Code extension owns. It needs the hub address and CSRF
// token, which is why hub discovery matters.
//
// Verified: a delivered message lands in the conversation transcript as a
// SYSTEM_MESSAGE, so it is visible in the VS Code chat.
type AgentAPI struct {
	cfg *Config
}

type apiEnvelope struct {
	Response struct {
		NewConversation struct {
			ConversationID string `json:"conversationId"`
		} `json:"newConversation"`
		SendMessage struct {
			RecipientID string `json:"recipientId"`
		} `json:"sendMessage"`
	} `json:"response"`
	Error string `json:"error"`
}

func (a *AgentAPI) run(h *Hub, args ...string) (*apiEnvelope, error) {
	// NOTE: the agy parser rejects --app_data_dir before the subcommand
	// ("unexpected argument"), so the subcommand MUST come first and the hub
	// is reached purely through env vars.
	cmd := exec.Command(a.cfg.AgyBin, args...)
	cmd.Dir = a.cfg.Workdir
	cmd.Env = append(os.Environ(),
		"HOME="+os.Getenv("HOME"),
		"ANTIGRAVITY_LS_ADDRESS="+h.Base(),
		"ANTIGRAVITY_CSRF_TOKEN="+h.CSRF,
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		env := &apiEnvelope{}
		if jerr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), env); jerr != nil {
			if err != nil {
				return nil, fmt.Errorf("agentapi: %v: %s", err, truncate(errb.String(), 300))
			}
			return nil, fmt.Errorf("agentapi: unparseable output %q", truncate(out.String(), 300))
		}
		if env.Error != "" {
			return env, fmt.Errorf("agentapi: %s", env.Error)
		}
		return env, nil
	case <-time.After(90 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("agentapi: timeout")
	}
}

// NewConversation starts a fresh Antigravity chat and returns its id.
func (a *AgentAPI) NewConversation(h *Hub, title, prompt, tier string) (string, error) {
	if tier == "" {
		tier = "flash"
	}
	env, err := a.run(h, "agentapi", "new-conversation",
		"--model="+tier, "--title="+title, prompt)
	if err != nil {
		return "", err
	}
	return env.Response.NewConversation.ConversationID, nil
}

// SendMessage pushes text into an existing conversation through the hub.
func (a *AgentAPI) SendMessage(h *Hub, convID, title, content string) error {
	args := []string{"agentapi", "send-message"}
	if title != "" {
		args = append(args, "--title="+title)
	}
	_, err := a.run(h, append(args, convID, content)...)
	return err
}

type convRow struct {
	ID       string
	Title    string
	Steps    int
	Modified string
}

// RecentConversations reads the hub-owned summaries DB strictly read-only.
func RecentConversations(geminiDir string, limit int) ([]convRow, error) {
	db := filepath.Join(geminiDir, "conversation_summaries.db")
	if _, err := os.Stat(db); err != nil {
		return nil, err
	}
	q := fmt.Sprintf("select conversation_id, title, step_count, last_modified_time "+
		"from conversation_summaries order by last_modified_time desc limit %d;", limit)
	out, err := exec.Command("sqlite3", "-separator", "\x1f", "file:"+db+"?mode=ro", q).Output()
	if err != nil {
		return nil, err
	}
	var rows []convRow
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) < 4 {
			continue
		}
		rows = append(rows, convRow{ID: f[0], Title: f[1], Steps: atoi(f[2]), Modified: f[3]})
	}
	return rows, nil
}

// LastActiveConversation returns the most recently touched chat that the bridge
// did not create itself, so /attach last finds your VS Code conversation.
func LastActiveConversation(geminiDir, prefix string) (convRow, bool) {
	rows, err := RecentConversations(geminiDir, 30)
	if err != nil {
		return convRow{}, false
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Title, prefix) {
			return r, true
		}
	}
	if len(rows) > 0 {
		return rows[0], true
	}
	return convRow{}, false
}
