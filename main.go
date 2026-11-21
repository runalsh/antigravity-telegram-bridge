package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func logf(format string, v ...any) {
	log.Printf(format, v...)
}

// Bridge wires Telegram to Antigravity. Two per-chat modes:
//
//	drive  — we own the conversation: one long-lived `agy` process, prompts from
//	         Telegram are real user steps, so the VS Code chat shows and continues
//	         them exactly like a locally typed message.
//	mirror — the chat belongs to VS Code; we only tail its transcript read-only.
type Bridge struct {
	cfg     *Config
	tg      *Tg
	st      *State
	hubs    *hubCache
	mu      sync.Mutex
	workers map[int64]*Worker
	watches map[int64]*Watcher
	lastOut map[int64]time.Time
	busy    map[int64]bool
	cdp     *CDPClient
}

func main() {
	// Prevent duplicate instances
	lockFile, err := os.OpenFile("/tmp/tgbridge.lock", os.O_CREATE|os.O_RDWR, 0666)
	if err == nil {
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			fmt.Fprintf(os.Stderr, "tgbridge is already running (failed to acquire lock)\n")
			os.Exit(0)
		}
	}

	cfgPath := flag.String("config", defaultConfigPath(), "path to config.json")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	if lf, err := os.OpenFile(cfg.StatePath+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
	}
	b := &Bridge{
		cfg:     cfg,
		tg:      NewTg(cfg.BotToken, cfg.APIBase),
		st:      newState(cfg.StatePath),
		hubs:    &hubCache{},
		workers: map[int64]*Worker{},
		watches: map[int64]*Watcher{},
		lastOut: map[int64]time.Time{},
		busy:    map[int64]bool{},
		cdp:     NewCDPClient(cfg.CDPURL),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		logf("signal received, shutting down")
		cancel()
		b.StopAll()
		os.Exit(0)
	}()

	if err := b.tg.Ping(); err != nil {
		logf("telegram ping failed: %v (will keep retrying)", err)
	}
	logf("tgbridge up: api=%s chat=%d workdir=%s mode=%s", cfg.APIBase, cfg.DefaultChat, cfg.Workdir, cfg.Mode)
	if h, err := b.hubs.get(); err == nil {
		logf("antigravity hub: pid=%d url=%s app_data_dir=%s", h.PID, h.Base(), h.AppDataDir)
	} else {
		logf("antigravity hub: %v", err)
	}

	// Restore watchers for known active chats
	for chatID, cs := range b.st.Chats {
		if cs != nil && cs.ConvID != "" {
			b.startWatch(chatID, cs)
		}
	}

	go b.reaper(ctx)
	if cfg.Driver == "cdp" {
		go b.cdpActionPoller(ctx)
	}
	b.pollLoop(ctx)
}

func (b *Bridge) cdpActionPoller(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastPromptSig string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prompt, err := b.cdp.DetectPendingActions(ctx)
			if err != nil || prompt == nil || len(prompt.Buttons) == 0 {
				lastPromptSig = ""
				continue
			}

			// Stable signature based on Command if present, else Question
			sig := prompt.Command
			if sig == "" {
				sig = prompt.Question
			}
			if sig == lastPromptSig {
				continue
			}
			lastPromptSig = sig

			var rows [][]map[string]string
			for _, btn := range prompt.Buttons {
				act := btn.Action
				if act == "" {
					act = fmt.Sprintf("%d", btn.Index)
				}
				rows = append(rows, []map[string]string{
					{
						"text":          btn.Text,
						"callback_data": fmt.Sprintf("action:%s", act),
					},
				})
			}
			keyboard := map[string]any{"inline_keyboard": rows}
			text := "🔔 <b>Confirmation required in VS Code:</b>"
			if prompt.Question != "" {
				text += "\n\n" + HTMLEscape(prompt.Question)
			}
			if prompt.Command != "" {
				text += "\n\n<code>" + HTMLEscape(prompt.Command) + "</code>"
			}
			logf("Sending pending action to TG: question=%q cmd=%q buttons=%+v", prompt.Question, prompt.Command, prompt.Buttons)
			for chatID := range b.st.Chats {
				b.tg.SendKeyboard(chatID, text, keyboard)
			}
		}
	}
}

func defaultConfigPath() string {
	if v := os.Getenv("TGBRIDGE_CONFIG"); v != "" {
		return v
	}
	return home() + "/tgbridge/config.json"
}

func home() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	return "/root"
}

func (b *Bridge) pollLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		ups, err := b.tg.GetUpdates(b.st.UpdateOK)
		if err != nil {
			logf("getUpdates: %v", err)
			sleepCtx(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, up := range ups {
			if up.UpdateID >= b.st.UpdateOK {
				b.st.UpdateOK = up.UpdateID + 1
			}

			// Handle inline keyboard button clicks
			if up.CallbackQuery.ID != "" {
				go b.handleCallback(ctx, up.CallbackQuery)
				continue
			}

			msg, ok := up.message()
			if !ok {
				continue
			}
			text := msg.Text
			if text == "" {
				text = msg.Caption
			}
			if text == "" {
				continue
			}
			chat := msg.Chat.ID
			if chat == 0 {
				chat = b.cfg.DefaultChat
			}
			if !b.cfg.allowed(chat) {
				logf("dropped message from unauthorized chat %d (text: %s)", chat, truncate(text, 60))
				continue
			}
			go b.handle(ctx, chat, text)
		}
		b.st.Save()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// StopAll tears down every child process.
func (b *Bridge) StopAll() {
	b.mu.Lock()
	ws := make([]*Worker, 0, len(b.workers))
	for _, w := range b.workers {
		ws = append(ws, w)
	}
	bs := make([]*Watcher, 0, len(b.watches))
	for _, w := range b.watches {
		bs = append(bs, w)
	}
	b.mu.Unlock()
	for _, w := range ws {
		w.Stop()
	}
	for _, w := range bs {
		w.Stop()
	}
	b.st.Save()
}

// reaper closes idle workers so they do not hold the conversation forever.
func (b *Bridge) reaper(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b.mu.Lock()
		var idle []int64
		for id, w := range b.workers {
			if w.Dead() || w.Idle() > time.Duration(b.cfg.WorkerIdleSec)*time.Second {
				idle = append(idle, id)
			}
		}
		b.mu.Unlock()
		for _, id := range idle {
			b.mu.Lock()
			w := b.workers[id]
			delete(b.workers, id)
			b.mu.Unlock()
			if w != nil && !w.Dead() {
				logf("idle worker %s closed", w.tag())
				w.Stop()
			}
		}
	}
}
