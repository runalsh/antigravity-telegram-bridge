package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanUserText(t *testing.T) {
	raw := `<USER_REQUEST>
hello world
</USER_REQUEST>
<ADDITIONAL_METADATA>
timestamp: 12345
</ADDITIONAL_METADATA>`

	cleaned := cleanUserText(raw)
	if cleaned != "hello world" {
		t.Errorf("expected 'hello world', got %q", cleaned)
	}

	plain := "just a plain message"
	if cleanUserText(plain) != plain {
		t.Errorf("expected %q, got %q", plain, cleanUserText(plain))
	}
}

func TestQuestionOptions(t *testing.T) {
	// 1. JSON string in questions field
	args := map[string]any{
		"questions": `[{"question":"Pick a fruit","options":["Apple","Banana","Cherry"]}]`,
	}
	opts := questionOptions(args)
	if len(opts) != 3 || opts[0] != "Apple" || opts[1] != "Banana" || opts[2] != "Cherry" {
		t.Fatalf("unexpected options: %#v", opts)
	}

	// 2. Direct slice of structs in questions field
	args2 := map[string]any{
		"questions": []map[string]any{
			{
				"question": "Choose mode",
				"options":  []string{"Auto", "Manual"},
			},
		},
	}
	opts2 := questionOptions(args2)
	if len(opts2) != 2 || opts2[0] != "Auto" || opts2[1] != "Manual" {
		t.Fatalf("unexpected options from map: %#v", opts2)
	}
}

func TestRenderQuestion(t *testing.T) {
	args := map[string]any{
		"questions": `[{"question":"Which database?","options":["PostgreSQL","ClickHouse"]}]`,
	}
	rendered := renderQuestion(args)
	expected := "❓ Which database?\n  1. PostgreSQL\n  2. ClickHouse"
	if rendered != expected {
		t.Errorf("renderQuestion = %q, want %q", rendered, expected)
	}
}

func TestTranscriptStepRender(t *testing.T) {
	userInput := transcriptStep{
		Type:    "USER_INPUT",
		Content: "<USER_REQUEST>run build</USER_REQUEST>",
	}
	if got := userInput.render(); got != "🧑 run build" {
		t.Errorf("expected '🧑 run build', got %q", got)
	}

	systemReminder := transcriptStep{
		Source:  "SYSTEM",
		Type:    "SYSTEM_MESSAGE",
		Content: "Reminder: work efficiently. Never one call per turn.",
	}
	if got := systemReminder.render(); got != "" {
		t.Errorf("expected system reminder to be filtered out, got %q", got)
	}
}

func TestWatcherTailing(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "transcript.jsonl")

	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var received []string
	var receivedOpts [][]string

	w := NewWatcher(logPath, 12345, 50*time.Millisecond,
		func(chat int64, line string) {
			received = append(received, line)
		},
		func(chat int64, opts []string) {
			receivedOpts = append(receivedOpts, opts)
		},
	)
	w.Start()
	defer w.Stop()

	// Append a USER_INPUT step
	step1 := transcriptStep{
		StepIndex: 1,
		Type:      "USER_INPUT",
		Content:   "Deploy to staging",
	}
	raw1, _ := json.Marshal(step1)
	f.Write(append(raw1, '\n'))

	// Append an ask_question step
	step2 := transcriptStep{
		StepIndex: 2,
		Source:    "MODEL",
		Type:      "PLANNER_RESPONSE",
		ToolCalls: []toolCall{
			{
				Name: "ask_question",
				Args: map[string]any{
					"questions": `[{"question":"Confirm deploy?","options":["Yes","No"]}]`,
				},
			},
		},
	}
	raw2, _ := json.Marshal(step2)
	f.Write(append(raw2, '\n'))
	f.Sync()

	time.Sleep(200 * time.Millisecond)

	if len(received) == 0 {
		t.Errorf("expected to receive rendered steps")
	}
	if len(receivedOpts) == 0 {
		t.Errorf("expected to trigger onQuestion callback")
	} else if len(receivedOpts[0]) != 2 || receivedOpts[0][0] != "Yes" {
		t.Errorf("unexpected options: %#v", receivedOpts[0])
	}
}
