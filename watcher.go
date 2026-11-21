package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// transcriptStep is one line of brain/<conv>/.system_generated/logs/transcript.jsonl.
// The hub writes this file for every conversation, including chats you drive
// inside VS Code — that is what makes mirroring possible without patching.
type transcriptStep struct {
	StepIndex int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	ToolCalls []toolCall `json:"tool_calls"`
}

// toolCall is one entry of the tool_calls array on a PLANNER_RESPONSE step.
type toolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// questionOptions extracts the answer options of an ask_question tool call.
func questionOptions(args map[string]any) []string {
	raw, ok := args["questions"].(string)
	if !ok {
		if b, err := json.Marshal(args["questions"]); err == nil {
			raw = string(b)
		} else {
			return nil
		}
	}
	var qs []struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &qs); err != nil {
		return nil
	}
	if len(qs) == 0 {
		return nil
	}
	return qs[0].Options
}

var (
	reMetadata   = regexp.MustCompile(`(?s)<ADDITIONAL_METADATA>.*?</ADDITIONAL_METADATA>`)
	reUserReq    = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)
	reSystemWrap = regexp.MustCompile("(?s)<SYSTEM_MESSAGE>.*?content=(.*)$")
)

// cleanUserText strips the prompt scaffolding the hub embeds in user steps.
func cleanUserText(s string) string {
	if m := reUserReq.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = reMetadata.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// renderQuestion formats an ask_question tool call into a Telegram-ready
// message: the question plus numbered answer options.
func renderQuestion(args map[string]any) string {
	raw, ok := args["questions"].(string)
	if !ok {
		if b, err := json.Marshal(args["questions"]); err == nil {
			raw = string(b)
		} else {
			return ""
		}
	}
	var qs []struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &qs); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, q := range qs {
		sb.WriteString("❓ " + q.Question + "\n")
		for i, o := range q.Options {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, o))
		}
	}
	return strings.TrimSpace(sb.String())
}

// render turns a transcript step into a Telegram-ready line, "" if uninteresting.
func (ts transcriptStep) render() string {
	switch {
	case ts.Type == "USER_INPUT":
		txt := cleanUserText(ts.Content)
		if txt == "" {
			return ""
		}
		return "🧑 " + txt
	case ts.Source == "SYSTEM" && ts.Type == "SYSTEM_MESSAGE":
		msg := strings.TrimSpace(ts.Content)
		// Ignore internal reminders and instructions sent to the model
		if strings.Contains(msg, "Reminder: work efficiently") || 
		   strings.Contains(msg, "not actually sent by the user") ||
		   strings.Contains(msg, "Never one call per turn") {
			return ""
		}
		if m := reSystemWrap.FindStringSubmatch(msg); m != nil {
			msg = strings.TrimSpace(m[1])
		}
		if msg == "" || strings.HasPrefix(msg, "[Notice]") {
			return ""
		}
		return "📩 " + msg
	case ts.Source == "MODEL" && ts.Type == "PLANNER_RESPONSE" && ts.Status == "DONE":
		// interactive tool calls (ask_question) carry the question + options;
		// they must reach Telegram even when the response text is empty
		for _, tc := range ts.ToolCalls {
			if tc.Name == "ask_question" {
				if q := renderQuestion(tc.Args); q != "" {
					return q
				}
			}
		}
		txt := strings.TrimSpace(ts.Content)
		if txt == "" {
			return ""
		}
		return "🤖 " + txt
	default:
		return "" // tool output and internals stay out of the chat
	}
}

// Watcher tails one transcript and forwards new visible steps to a callback.
type Watcher struct {
	path    string
	chat    int64
	every   time.Duration
	onLine  func(int64, string)
	onQuestion func(int64, []string) // fires when an ask_question tool call arrives
	stop    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	offset  int64
	handled map[int]bool
}

func NewWatcher(path string, chat int64, every time.Duration,
	onLine func(int64, string), onQuestion func(int64, []string)) *Watcher {
	return &Watcher{path: path, chat: chat, every: every, onLine: onLine,
		onQuestion: onQuestion, stop: make(chan struct{}), handled: map[int]bool{}}
}

// SkipToTail positions the reader at EOF so a fresh watch does not replay the
// entire history into Telegram.
func (w *Watcher) SkipToTail() {
	if fi, err := os.Stat(w.path); err == nil {
		w.mu.Lock()
		w.offset = fi.Size()
		w.mu.Unlock()
	}
}

// CatchUp jumps to current EOF after a drive turn: steps the turn produced are
// already streamed to Telegram by the turn itself, so the watcher must not
// replay them.
func (w *Watcher) CatchUp() {
	if fi, err := os.Stat(w.path); err == nil {
		w.mu.Lock()
		if fi.Size() > w.offset {
			w.offset = fi.Size()
		}
		w.mu.Unlock()
	}
}

func (w *Watcher) Start() {
	go func() {
		t := time.NewTicker(w.every)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.poll()
			}
		}
	}()
}

func (w *Watcher) Stop() { w.once.Do(func() { close(w.stop) }) }

func (w *Watcher) poll() {
	f, err := os.Open(w.path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}

	w.mu.Lock()
	offset := w.offset
	w.mu.Unlock()
	if fi.Size() < offset { // truncated / rewritten
		offset = 0
		w.mu.Lock()
		w.handled = map[int]bool{}
		w.mu.Unlock()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}

	rd := bufio.NewReaderSize(f, 2<<20)
	consumed := offset
	for {
		line, rerr := rd.ReadString('\n')
		if line != "" && strings.HasSuffix(line, "\n") {
			consumed += int64(len(line))
			w.handle([]byte(line))
		}
		if rerr != nil {
			break // an incomplete tail line is simply re-read next tick
		}
	}
	w.mu.Lock()
	w.offset = consumed
	w.mu.Unlock()
}

// handle parses one complete JSONL record and forwards it at most once.
func (w *Watcher) handle(raw []byte) {
	var ts transcriptStep
	if err := json.Unmarshal(bytes.TrimSpace(raw), &ts); err != nil {
		return
	}
	w.mu.Lock()
	if w.handled[ts.StepIndex] {
		w.mu.Unlock()
		return
	}
	w.handled[ts.StepIndex] = true
	w.mu.Unlock()

	if txt := ts.render(); txt != "" {
		w.onLine(w.chat, txt)
	}
	if w.onQuestion != nil {
		for _, tc := range ts.ToolCalls {
			if tc.Name == "ask_question" {
				if opts := questionOptions(tc.Args); len(opts) > 0 {
					w.onQuestion(w.chat, opts)
				}
			}
		}
	}
}
