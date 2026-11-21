package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const helpText = `<b>antigravity-telegram-bridge</b> — Telegram ↔ Antigravity bridge

<b>Chat</b>
  &lt;text&gt;       — send to agent (drive mode)
  /send &lt;..&gt;   — inject into IDE chat via hub (mirror mode)
  /stop          — stop current turn
  /new [title]   — new conversation thread (drive)
  /attach &lt;id|last&gt; — connect to existing chat
  /mirror        — watch current VS Code chat read-only
  /detach        — disconnect mirroring

<b>Lists & Status</b>
  /ls [n]        — list recent Antigravity chats
  /status        — mode, conversation, hub, worker
  /mode [plan|accept-edits] — agent mode for new workers
  /model [tag]   — model for new workers
  /cd &lt;path&gt;   — worker working directory
  /id            — ID of current chat / conversation
  /driver [cdp|hub] — switch input driver
  /chats         — list open webview conversations
  /actions       — check pending buttons/prompts
  /ping          — check Telegram and hub connection
  /help          — show this help message

In drive mode, messages sent from VS Code and Telegram share the exact same conversation.`

// handle processes a single incoming text message from Telegram.
func (b *Bridge) handle(ctx context.Context, chat int64, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if !b.cfg.allowed(chat) {
		logf("ignoring message from unlisted chat %d", chat)
		return
	}
	cs := b.st.chat(chat)

	if strings.HasPrefix(text, "/") {
		b.command(ctx, chat, cs, text)
		return
	}

	// A pending ask_question: "1".."N" maps onto the numbered option shown in
	// Telegram; any other text is treated as a free-form answer. Either way the
	// pending options are consumed and the chosen text goes to the agent.
	if len(cs.Pending) > 0 {
		if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(cs.Pending) {
			text = cs.Pending[n-1]
		}
		cs.Pending = nil
		b.st.setChat(chat, *cs)
	}

	// CDP mode: directly inject into VS Code chat input in the DOM
	if b.cfg.Driver == "cdp" {
		if err := b.cdp.Connect(); err != nil {
			b.say(chat, "cdp connection error: %v\nFalling back to hub...", err)
		} else {
			if err := b.cdp.SendPrompt(ctx, text); err != nil {
				b.say(chat, "cdp input error: %v", err)
			} else {
				logf("injected text via CDP into VS Code DOM: %s", truncate(text, 50))
			}
			return
		}
	}

	// Mirror mode: the conversation is owned by the IDE hub, so plain text is
	// injected through the hub — that is the only path the VS Code UI renders
	// live.
	if cs.Mirror {
		b.sendViaHub(chat, cs, text)
		return
	}

	b.mu.Lock()
	if b.busy[chat] {
		b.mu.Unlock()
		b.say(chat, "⏳ agent is still thinking. Wait for response or /stop")
		return
	}
	b.busy[chat] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.busy, chat)
		b.mu.Unlock()
	}()
	b.driveTurn(ctx, chat, cs, text)
}

// sendViaHub injects text into the attached conversation through the IDE hub.
// The agent answers within the IDE session, and the mirror watcher forwards
// the reply back to Telegram.
func (b *Bridge) sendViaHub(chat int64, cs *ChatState, text string) {
	if cs.ConvID == "" {
		b.say(chat, "conversation not selected — /attach last or /mirror")
		return
	}
	h, err := b.hubs.get()
	if err != nil {
		b.say(chat, "%v", err)
		return
	}
	if err := (&AgentAPI{cfg: b.cfg}).SendMessage(h, cs.ConvID, "from-telegram", text); err != nil {
		b.say(chat, "%v", err)
		return
	}
	b.say(chat, "📩 delivered to VS Code chat — response will arrive here")
}

// say sends a plain message, escaping HTML.
func (b *Bridge) say(chat int64, format string, v ...any) {
	txt := HTMLEscape(fmt.Sprintf(format, v...))
	if _, err := b.tg.Send(chat, txt, "HTML"); err != nil {
		logf("send to %d: %v", chat, err)
	}
}

// sayRaw sends text that is already HTML-safe.
func (b *Bridge) sayRaw(chat int64, html string) {
	if err := b.tg.SendChunked(chat, html, "HTML"); err != nil {
		logf("send to %d: %v", chat, err)
	}
}

func (b *Bridge) driveTurn(ctx context.Context, chat int64, cs *ChatState, prompt string) {
	w, err := b.ensureWorker(ctx, chat, cs)
	if err != nil {
		b.say(chat, "❌ %v", err)
		return
	}
	progress := newProgress(b, chat, b.cfg.Stream && b.cfg.ToolProgress)
	res, err := w.Turn(ctx, prompt, progress.onEvent)
	progress.finish()
	b.catchUp(chat) // watcher must not replay steps the turn already streamed
	if err != nil {
		b.say(chat, "❌ %v", err)
		return
	}
	if res.Result.Status != "" && res.Result.Status != "SUCCESS" {
		body := firstNonEmpty(res.Result.Error, res.Result.Response)
		b.say(chat, "⚠️ status %s: %s", res.Result.Status, truncate(body, 1200))
		return
	}
	answer := strings.TrimSpace(res.Result.Response)
	if answer == "" {
		answer = "(empty response)"
	}
	logf("turn done chat=%d conv=%s status=%s len=%d", chat, shortID(w.ConvID),
		res.Result.Status, len(answer))
	if !progress.emitted {
		b.say(chat, "🤖 %s", answer)
		return
	}
	if err := b.tg.Edit(chat, progress.msgID, "🤖 "+answer, ""); err != nil {
		b.say(chat, "🤖 %s", answer)
	}
}

// ensureWorker returns a live worker for this chat, starting one on demand.
// The very first turn of a fresh conversation tells us its id via "init".
func (b *Bridge) ensureWorker(ctx context.Context, chat int64, cs *ChatState) (*Worker, error) {
	b.mu.Lock()
	w := b.workers[chat]
	b.mu.Unlock()
	if w != nil && !w.Dead() {
		return w, nil
	}
	conv := cs.ConvID
	w = NewWorker(b.cfg, conv, b.workdir(chat), b.cfg.Mode, b.cfg.Model)
	if err := w.Start(ctx); err != nil {
		return nil, err
	}
	cs.ConvID = w.ConvID
	if cs.Title == "" {
		cs.Title = "tg " + shortID(w.ConvID)
	}
	b.st.setChat(chat, *cs)
	b.mu.Lock()
	b.workers[chat] = w
	b.mu.Unlock()
	b.startWatch(chat, cs)
	logf("chat %d -> worker %s (drive)", chat, w.tag())
	return w, nil
}

func (b *Bridge) workdir(chat int64) string {
	if cs := b.st.chat(chat); cs != nil && cs.Cwd != "" {
		return cs.Cwd
	}
	return b.cfg.Workdir
}

func (b *Bridge) stopWorker(chat int64) {
	b.mu.Lock()
	w := b.workers[chat]
	delete(b.workers, chat)
	b.mu.Unlock()
	if w != nil {
		w.Stop()
	}
}

// catchUp advances the transcript watcher (if any) to EOF so steps already
// streamed by a drive turn are not duplicated.
func (b *Bridge) catchUp(chat int64) {
	b.mu.Lock()
	w := b.watches[chat]
	b.mu.Unlock()
	if w != nil {
		w.CatchUp()
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func shortDur(d time.Duration) string { return d.Round(time.Second).String() }

func (b *Bridge) handleCallback(ctx context.Context, cb TgCallbackQuery) {
	data := cb.Data
	chat := cb.Message.Chat.ID
	if chat == 0 {
		chat = b.cfg.DefaultChat
	}
	logf("handleCallback: data=%q chat=%d", data, chat)

	if strings.HasPrefix(data, "opt:") {
		idxStr := strings.TrimPrefix(data, "opt:")
		idx, err := strconv.Atoi(idxStr)
		cs := b.st.chat(chat)
		if err == nil && idx >= 0 && idx < len(cs.Pending) {
			choice := cs.Pending[idx]
			cs.Pending = nil
			b.st.setChat(chat, *cs)
			b.tg.AnswerCallbackQuery(cb.ID, "Selected: "+choice)
			b.sayRaw(chat, fmt.Sprintf("👉 <i>Selected option:</i> <b>%s</b>", HTMLEscape(choice)))
			go func() {
				if b.cfg.Driver == "cdp" {
					if err := b.cdp.Connect(); err == nil {
						if err := b.cdp.AnswerQuestion(ctx, choice, idx); err == nil {
							logf("answered question via CDP form: %q (idx=%d)", choice, idx)
							return
						}
					}
				}
				b.handle(ctx, chat, choice)
			}()
			return
		}
		b.tg.AnswerCallbackQuery(cb.ID, "Option expired")
		return
	}

	if strings.HasPrefix(data, "action:") {
		action := strings.TrimPrefix(data, "action:")
		if err := b.cdp.Connect(); err != nil {
			b.tg.AnswerCallbackQuery(cb.ID, "CDP error")
			return
		}
		if err := b.cdp.ClickAction(ctx, action); err != nil {
			b.tg.AnswerCallbackQuery(cb.ID, "Click error: "+err.Error())
			return
		}
		b.tg.AnswerCallbackQuery(cb.ID, "Action completed!")
		b.say(chat, "✅ Button clicked in VS Code")
		return
	}

	if strings.HasPrefix(data, "chat:") {
		convID := strings.TrimPrefix(data, "chat:")
		cs := b.st.chat(chat)
		cs.ConvID = convID
		cs.Mirror = true
		b.st.setChat(chat, *cs)
		b.stopWorker(chat)
		b.startWatch(chat, cs)
		b.tg.AnswerCallbackQuery(cb.ID, "Conversation switched!")
		b.say(chat, "🗂️ Switched to conversation <code>%s</code> (mirror mode)", shortID(convID))
		return
	}

	b.tg.AnswerCallbackQuery(cb.ID, "")
}

