package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Worker owns one long-lived `agy` session bound to a conversation. One process
// serves many turns: each turn is one NDJSON line on stdin and ends with the
// matching "result" event on stdout. Context is preserved between turns.
type Worker struct {
	cfg   *Config
	conv  string // conversation to resume; "" means let agy create one
	cwd   string
	mode  string
	model string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan streamEvent
	stderr *bytes.Buffer

	ConvID   string
	lastUsed time.Time
	dead     bool
	busy     bool
	mu       sync.Mutex
}

func NewWorker(cfg *Config, conv, cwd, mode, model string) *Worker {
	return &Worker{cfg: cfg, conv: conv, cwd: cwd, mode: mode, model: model,
		lines: make(chan streamEvent, 256)}
}

func (w *Worker) args() []string {
	a := []string{"--app_data_dir=" + w.cfg.AppDataDir}
	if w.conv != "" {
		a = append(a, "--conversation="+w.conv)
	}
	if w.mode != "" {
		a = append(a, "--mode="+w.mode)
	}
	if w.model != "" {
		a = append(a, "--model="+w.model)
	}
	if w.cfg.SkipPerms {
		a = append(a, "--dangerously-skip-permissions")
	}
	a = append(a, "--input-format", "stream-json", "--output-format", "stream-json")
	return a
}

// Start launches agy and waits for "init" so we learn the conversation id.
func (w *Worker) Start(ctx context.Context) error {
	if _, err := os.Stat(w.cfg.AgyBin); err != nil {
		return fmt.Errorf("agy binary missing: %v", err)
	}
	w.cmd = exec.Command(w.cfg.AgyBin, w.args()...)
	w.cmd.Dir = w.cwd
	w.cmd.Env = append(os.Environ(), "HOME="+os.Getenv("HOME"), "TERM=dumb", "NO_COLOR=1")
	stdin, err := w.cmd.StdinPipe()
	if err != nil {
		return err
	}
	w.stdin = stdin
	stdout, err := w.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	w.stderr = &bytes.Buffer{}
	w.cmd.Stderr = w.stderr
	if err := w.cmd.Start(); err != nil {
		return err
	}
	go w.readLoop(stdout)
	go w.reap()
	w.lastUsed = time.Now()

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-w.lines:
			if !ok {
				return fmt.Errorf("agy exited during startup: %s", truncate(w.stderr.String(), 400))
			}
			if ev.Event == "init" {
				w.ConvID = ev.ConversationID
				if w.ConvID == "" {
					w.ConvID = w.conv
				}
				return nil
			}
		case <-timer.C:
			w.Stop()
			return fmt.Errorf("agy did not report init within 120s")
		case <-ctx.Done():
			w.Stop()
			return ctx.Err()
		}
	}
}

func (w *Worker) readLoop(r io.Reader) {
	defer close(w.lines)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue // TUI leftovers; irrelevant in stream mode
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		select {
		case w.lines <- ev:
		case <-time.After(15 * time.Second):
			// consumer vanished; drop instead of blocking the pipe forever
		}
	}
}

func (w *Worker) reap() {
	err := w.cmd.Wait()
	w.mu.Lock()
	w.dead = true
	w.mu.Unlock()
	if err != nil {
		logf("worker %s: agy exited: %v | %s", w.tag(), err, truncate(w.stderr.String(), 300))
	}
}

// Turn sends one prompt and streams events until the matching "result".
// onEvent receives every non-result event; keep it fast.
func (w *Worker) Turn(ctx context.Context, prompt string, onEvent func(streamEvent)) (streamEvent, error) {
	w.mu.Lock()
	switch {
	case w.dead:
		w.mu.Unlock()
		return streamEvent{}, fmt.Errorf("worker is not running")
	case w.busy:
		w.mu.Unlock()
		return streamEvent{}, fmt.Errorf("agent is busy — check /status or send /stop")
	}
	w.busy = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.busy = false
		w.lastUsed = time.Now()
		w.mu.Unlock()
	}()

drain:
	for { // drop leftovers from an aborted previous turn
		select {
		case _, ok := <-w.lines:
			if !ok {
				return streamEvent{}, fmt.Errorf("stream closed")
			}
			continue
		default:
			break drain
		}
	}

	if _, err := w.stdin.Write([]byte(userMessage(prompt))); err != nil {
		return streamEvent{}, fmt.Errorf("write to agy: %w", err)
	}
	timer := time.NewTimer(time.Duration(w.cfg.TurnTimeoutSec) * time.Second)
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-w.lines:
			if !ok {
				return streamEvent{}, fmt.Errorf("agy closed the stream (%s)", truncate(w.stderr.String(), 300))
			}
			if ev.Event == "result" {
				if ev.ConversationID != "" {
					w.ConvID = ev.ConversationID
				}
				return ev, nil
			}
			if onEvent != nil {
				onEvent(ev)
			}
		case <-timer.C:
			return streamEvent{}, fmt.Errorf("turn timed out after %ds", w.cfg.TurnTimeoutSec)
		case <-ctx.Done():
			return streamEvent{}, ctx.Err()
		}
	}
}

func (w *Worker) tag() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ConvID != "" {
		return shortID(w.ConvID)
	}
	return shortID(w.conv)
}

func (w *Worker) Idle() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.lastUsed)
}

func (w *Worker) Busy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy
}

func (w *Worker) Dead() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dead
}

// Stop closes stdin so agy exits gracefully, then force-kills if it lingers.
func (w *Worker) Stop() {
	w.mu.Lock()
	stdin, cmd := w.stdin, w.cmd
	w.dead = true
	w.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	go func() {
		time.Sleep(8 * time.Second)
		if pidAlive(cmd.Process.Pid) {
			_ = cmd.Process.Kill()
		}
	}()
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
