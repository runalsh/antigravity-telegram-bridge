package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdAttach binds this Telegram chat to an existing Antigravity conversation
// and takes it over: the bridge drives it, so your VS Code window keeps
// showing the very same thread (same conversation db, same transcript).
func (b *Bridge) cmdAttach(ctx context.Context, chat int64, cs *ChatState, rest string) {
	id, err := b.resolveConversation(chat, rest)
	if err != nil {
		b.say(chat, "%v", err)
		return
	}
	b.stopWatch(chat)
	b.stopWorker(chat)
	cs.ConvID = id
	cs.Mirror = false
	cs.TLMsg = 0
	b.st.setChat(chat, *cs)
	w, err := b.ensureWorker(ctx, chat, cs)
	if err != nil {
		b.say(chat, "failed to connect: %v", err)
		return
	}
	b.say(chat, "connected to <code>%s</code> — context preserved, send a message to proceed", shortID(w.ConvID))
}

// resolveConversation maps "last" or an id prefix onto a conversation id.
func (b *Bridge) resolveConversation(chat int64, arg string) (string, error) {
	gdir := b.cfg.geminiDir()
	if arg == "" || arg == "last" {
		if r, ok := LastActiveConversation(gdir, b.cfg.TitlePrefix); ok {
			return r.ID, nil
		}
		return "", fmt.Errorf("no conversations found")
	}
	rows, err := RecentConversations(gdir, 200)
	if err != nil {
		return "", err
	}
	arg = strings.ToLower(arg)
	var hits []convRow
	for _, r := range rows {
		if strings.HasPrefix(strings.ToLower(r.ID), arg) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("conversation %s not found (list — /ls)", arg)
	case 1:
		return hits[0].ID, nil
	default:
		return "", fmt.Errorf("prefix %s is ambiguous: %d matches", arg, len(hits))
	}
}

// cmdMirror attaches to a conversation the VS Code extension drives: we only
// tail its transcript, because that session belongs to the IDE hub.
func (b *Bridge) cmdMirror(chat int64, cs *ChatState, rest string) {
	id, err := b.resolveConversation(chat, rest)
	if err != nil {
		b.say(chat, "%v", err)
		return
	}
	b.stopWorker(chat)
	cs.ConvID = id
	cs.Mirror = true
	cs.Pending = nil
	b.st.setChat(chat, *cs)
	b.startWatch(chat, cs)
	b.say(chat, "mirror: watching <code>%s</code> — VS Code messages, responses and ask_question forms will arrive here; reply with option number", shortID(id))
}

// cmdSend injects text into the attached conversation through the hub — the
// same path as mirror-mode plain text, available in drive mode too.
func (b *Bridge) cmdSend(chat int64, cs *ChatState, rest string) {
	if cs.ConvID == "" {
		b.say(chat, "conversation not selected — /attach last")
		return
	}
	if rest == "" {
		b.say(chat, "usage: /send &lt;text&gt;")
		return
	}
	b.sendViaHub(chat, cs, rest)
}

func (b *Bridge) cmdModel(chat int64, cs *ChatState, rest string) {
	if rest == "" {
		b.say(chat, "model: %s (empty — inherited from agy /config)", none(b.cfg.Model))
		return
	}
	b.cfg.Model = rest
	b.stopWorker(chat)
	b.say(chat, "model: %s (will apply on next message)", rest)
}

func (b *Bridge) cmdCd(chat int64, cs *ChatState, rest string) {
	if rest == "" {
		b.say(chat, "cwd: %s", b.workdir(chat))
		return
	}
	p := rest
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.cfg.Workdir, p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		b.say(chat, "directory not accessible: %s", p)
		return
	}
	cs.Cwd = p
	b.st.setChat(chat, *cs)
	b.stopWorker(chat)
	b.say(chat, "cwd: %s (will apply on next message)", p)
}

// startWatch begins mirroring the conversation transcript into Telegram.
func (b *Bridge) startWatch(chat int64, cs *ChatState) {
	if !b.cfg.WatchTranscripts || cs.ConvID == "" {
		return
	}
	b.stopWatch(chat)
	path := conversationTranscript(b.cfg.geminiDir(), cs.ConvID)
	w := NewWatcher(path, chat, time.Duration(b.cfg.WatchIntervalMs)*time.Millisecond,
		func(chatID int64, line string) {
			b.mu.Lock()
			last := b.lastOut[chatID]
			busy := b.busy[chatID]
			b.mu.Unlock()
			// In drive mode our own turn streams content to Telegram already;
			// the watcher must stay quiet while busy or right after a turn.
			// In mirror mode everything is forwarded regardless.
			if !cs.Mirror && (busy || time.Since(last) < 3*time.Second) {
				return
			}
			b.sayRaw(chatID, HTMLEscape(line))
		},
		func(chatID int64, opts []string) {
			logf("onQuestion triggered for chat %d with %d options: %v", chatID, len(opts), opts)
			cur := b.st.chat(chatID)
			cur.Pending = opts
			b.st.Save()

			if len(opts) > 0 {
				var rows [][]map[string]string
				for i, opt := range opts {
					rows = append(rows, []map[string]string{
						{
							"text":          opt,
							"callback_data": fmt.Sprintf("opt:%d", i),
						},
					})
				}
				keyboard := map[string]any{"inline_keyboard": rows}
				msgID, err := b.tg.SendKeyboard(chatID, "❓ <b>Select an option:</b>", keyboard)
				logf("SendKeyboard result: msgID=%d err=%v", msgID, err)
			}
		})
	w.SkipToTail()
	w.Start()
	b.mu.Lock()
	b.watches[chat] = w
	b.mu.Unlock()
	logf("chat %d watching %s", chat, path)
}

func (b *Bridge) stopWatch(chat int64) {
	b.mu.Lock()
	w := b.watches[chat]
	delete(b.watches, chat)
	b.mu.Unlock()
	if w != nil {
		w.Stop()
	}
}
