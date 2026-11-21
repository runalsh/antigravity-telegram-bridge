package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// command dispatches slash commands.
func (b *Bridge) command(ctx context.Context, chat int64, cs *ChatState, text string) {
	fields := strings.Fields(text)
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))

	switch name {
	case "help", "start":
		b.sayRaw(chat, helpText)
	case "ping":
		b.cmdPing(chat)
	case "id":
		b.cmdID(chat, cs)
	case "status":
		b.cmdStatus(chat, cs)
	case "ls":
		b.cmdList(chat, rest)
	case "new":
		b.cmdNew(chat, cs, rest)
	case "attach":
		b.cmdAttach(ctx, chat, cs, rest)
	case "detach":
		b.stopWatch(chat)
		b.stopWorker(chat)
		b.st.forget(chat)
		b.say(chat, "detached. /attach last to reconnect")
	case "mirror":
		b.cmdMirror(chat, cs, rest)
	case "send":
		b.cmdSend(chat, cs, rest)
	case "stop":
		b.stopWorker(chat)
		b.say(chat, "worker stopped")
	case "mode":
		b.cmdMode(chat, cs, rest)
	case "model":
		b.cmdModel(chat, cs, rest)
	case "cd":
		b.cmdCd(chat, cs, rest)
	case "driver":
		b.cmdDriver(chat, rest)
	case "chats":
		b.cmdChats(ctx, chat)
	case "actions", "ask":
		b.cmdActions(ctx, chat)
	default:
		b.say(chat, "unknown command /%s — /help", name)
	}
}

func (b *Bridge) cmdPing(chat int64) {
	h, herr := b.hubs.get()
	if err := b.tg.Ping(); err != nil {
		b.say(chat, "telegram: ❌ %v", err)
		return
	}
	if herr != nil {
		b.say(chat, "telegram: ✅\nantigravity: ❌ %v", herr)
		return
	}
	b.say(chat, "telegram: ✅\nantigravity hub: ✅ pid=%d %s (app_data_dir=%s)", h.PID, h.Base(), h.AppDataDir)
}

func (b *Bridge) cmdID(chat int64, cs *ChatState) {
	mode := "drive"
	if cs.Mirror {
		mode = "mirror"
	}
	b.say(chat, "tg chat: %d\nmode: %s\nconversation: %s\ntitle: %s\ncwd: %s",
		chat, mode, none(cs.ConvID), none(cs.Title), b.workdir(chat))
}

func (b *Bridge) cmdStatus(chat int64, cs *ChatState) {
	b.mu.Lock()
	w := b.workers[chat]
	b.mu.Unlock()
	ws := "no active worker"
	if w != nil {
		ws = fmt.Sprintf("worker %s: busy=%v idle=%s", w.tag(), w.Busy(), shortDur(w.Idle()))
	}
	h, herr := b.hubs.get()
	hub := "hub: unavailable"
	if herr == nil {
		hub = fmt.Sprintf("hub: pid=%d %s", h.PID, h.Base())
	}
	mode := "drive (agent spawned from here; visible in VS Code)"
	if cs.Mirror {
		mode = "mirror (read-only transcript)"
	}
	b.say(chat, "%s\n%s\nconversation: %s\nagent mode: %s | model: %s\n%s",
		mode, hub, none(cs.ConvID), b.cfg.Mode, none(b.cfg.Model), ws)
}

func (b *Bridge) cmdList(chat int64, rest string) {
	limit := 10
	if rest != "" {
		if n, err := strconv.Atoi(rest); err == nil && n > 0 && n <= 40 {
			limit = n
		}
	}
	rows, err := RecentConversations(b.cfg.geminiDir(), limit)
	if err != nil {
		b.say(chat, "failed to read chat list: %v", err)
		return
	}
	current := b.st.chat(chat).ConvID
	var sb strings.Builder
	sb.WriteString("<b>recent conversations</b>\n")
	for _, r := range rows {
		mark := "・"
		if r.ID == current {
			mark = "▶"
		}
		title := r.Title
		if title == "" {
			title = "(no title)"
		}
		mod := r.Modified
		if len(mod) >= 19 {
			mod = mod[:19]
		}
		sb.WriteString(fmt.Sprintf("%s <code>%s</code> %s — %s, %d steps\n",
			mark, shortID(r.ID), HTMLEscape(truncate(title, 40)), mod, r.Steps))
	}
	sb.WriteString("\n/attach &lt;id prefix|last&gt; — connect")
	b.sayRaw(chat, sb.String())
}

func (b *Bridge) cmdNew(chat int64, cs *ChatState, rest string) {
	b.stopWatch(chat)
	b.stopWorker(chat)
	title := firstNonEmpty(rest, "tg chat")
	if !strings.HasPrefix(title, b.cfg.TitlePrefix) {
		title = b.cfg.TitlePrefix + " " + title
	}
	cs.ConvID = ""
	cs.Title = title
	cs.Mirror = false
	cs.TLMsg = 0
	b.st.setChat(chat, *cs)
	b.say(chat, "new conversation will be created on first message. title: %s", title)
}

func (b *Bridge) cmdMode(chat int64, cs *ChatState, rest string) {
	allowed := []string{"plan", "accept-edits"}
	if rest == "" {
		b.say(chat, "agent mode: %s (available: %s)", b.cfg.Mode, strings.Join(allowed, "|"))
		return
	}
	for _, a := range allowed {
		if a == rest {
			b.cfg.Mode = rest
			b.stopWorker(chat)
			b.say(chat, "mode: %s (will apply on next message)", rest)
			return
		}
	}
	b.say(chat, "unsupported mode. available: %s", strings.Join(allowed, "|"))
}

func none(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (b *Bridge) cmdDriver(chat int64, rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		b.say(chat, "current input driver: <b>%s</b> (available: hub | cdp)", b.cfg.Driver)
		return
	}
	if rest != "hub" && rest != "cdp" {
		b.say(chat, "unknown driver. Available: hub, cdp")
		return
	}
	b.cfg.Driver = rest
	if rest == "cdp" {
		if err := b.cdp.Connect(); err != nil {
			b.say(chat, "driver switched to cdp, but connection failed: %v", err)
			return
		}
		b.say(chat, "driver switched to <b>cdp</b> (direct VS Code DOM input)")
	} else {
		b.say(chat, "driver switched to <b>hub</b> (background API inject)")
	}
}

func (b *Bridge) cmdChats(ctx context.Context, chat int64) {
	if err := b.cdp.Connect(); err != nil {
		b.say(chat, "cdp error: %v", err)
		return
	}
	convs, err := b.cdp.ListConversations(ctx)
	if err != nil {
		b.say(chat, "failed to get chat list: %v", err)
		return
	}
	if len(convs) == 0 {
		b.say(chat, "no active conversations found in UI")
		return
	}

	var rows [][]map[string]string
	for _, c := range convs {
		title := c.Title
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		rows = append(rows, []map[string]string{
			{
				"text":          title,
				"callback_data": "chat:" + c.ID,
			},
		})
	}

	keyboard := map[string]any{"inline_keyboard": rows}
	b.tg.SendKeyboard(chat, "🗂️ <b>Select conversation to continue:</b>", keyboard)
}

func (b *Bridge) cmdActions(ctx context.Context, chat int64) {
	if err := b.cdp.Connect(); err != nil {
		b.say(chat, "cdp error: %v", err)
		return
	}
	prompt, err := b.cdp.DetectPendingActions(ctx)
	if err != nil {
		b.say(chat, "failed to check actions: %v", err)
		return
	}
	if len(prompt.Buttons) == 0 {
		b.say(chat, "no pending buttons/prompts in interface")
		return
	}

	var rows [][]map[string]string
	for _, btn := range prompt.Buttons {
		rows = append(rows, []map[string]string{
			{
				"text":          btn.Text,
				"callback_data": fmt.Sprintf("action:%d", btn.Index),
			},
		})
	}

	text := "🔔 <b>Action required in VS Code:</b>"
	if prompt.Question != "" {
		text += "\n\n" + HTMLEscape(prompt.Question)
	}

	keyboard := map[string]any{"inline_keyboard": rows}
	b.tg.SendKeyboard(chat, text, keyboard)
}

