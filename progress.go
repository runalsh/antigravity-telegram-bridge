package main

import (
	"strings"
	"sync"
	"time"
)

// progress renders agent activity into a single editable Telegram message:
// tool calls as they happen, then the streaming answer.
type progress struct {
	b       *Bridge
	chat    int64
	enabled bool

	mu       sync.Mutex
	msgID    int64
	text     strings.Builder
	tools    []string
	lastEdit time.Time
	emitted  bool
}

func newProgress(b *Bridge, chat int64, enabled bool) *progress {
	return &progress{b: b, chat: chat, enabled: enabled}
}

func (p *progress) onEvent(ev streamEvent) {
	if !p.enabled {
		return
	}
	su := ev.StepUpdate
	switch {
	case su.StepType == "agent_response" && su.State == "ACTIVE" && su.TextDelta != "":
		p.mu.Lock()
		p.text.WriteString(su.TextDelta)
		p.mu.Unlock()
		p.maybeEdit("🤖 " + p.snapshot())
	case su.StepType == "tool" && su.State == "ACTIVE":
		name := su.ToolName
		if name == "" {
			name = su.ToolInfo.Name
		}
		detail := toolDetail(su.ToolInfo.Parameters)
		p.mu.Lock()
		p.tools = append(p.tools, name+" "+detail)
		if len(p.tools) > 6 {
			p.tools = p.tools[len(p.tools)-6:]
		}
		p.mu.Unlock()
		p.forceEdit(p.progressLine())
	case su.StepType == "user_input" || su.StepType == "system_message":
		// ignore echo steps, they would only add noise
	}
}

func (p *progress) snapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return truncateAll(p.text.String(), 3500)
}

func (p *progress) progressLine() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := make([]string, 0, len(p.tools))
	for _, t := range p.tools {
		lines = append(lines, "• "+t)
	}
	body := strings.Join(lines, "\n")
	if body == "" {
		body = "…thinking"
	}
	if p.text.Len() > 0 {
		return "🤖 " + truncateAll(p.text.String(), 2000) + "\n\n🔧 " + body
	}
	return "🔧 " + body
}

func (p *progress) maybeEdit(text string) {
	p.mu.Lock()
	if time.Since(p.lastEdit) < time.Duration(p.b.cfg.StreamEditMs)*time.Millisecond {
		p.mu.Unlock()
		return
	}
	p.lastEdit = time.Now()
	p.mu.Unlock()
	p.push(text)
}

func (p *progress) forceEdit(text string) {
	p.mu.Lock()
	p.lastEdit = time.Now()
	p.mu.Unlock()
	p.push(text)
}

func (p *progress) push(text string) {
	if p.msgID == 0 {
		id, err := p.b.tg.Send(p.chat, text, "")
		if err != nil {
			return
		}
		p.msgID = id
		p.emitted = true
		return
	}
	_ = p.b.tg.Edit(p.chat, p.msgID, text, "")
	p.emitted = true
}

func (p *progress) finish() {
	p.mu.Lock()
	id := p.msgID
	p.mu.Unlock()
	p.b.mu.Lock()
	if id != 0 {
		p.b.lastOut[p.chat] = time.Now()
	}
	p.b.mu.Unlock()
}

// toolDetail picks the most informative single argument of a tool call.
func toolDetail(params map[string]any) string {
	for _, k := range []string{"CommandLine", "command", "Path", "path", "AbsolutePath", "query", "pattern", "Url", "url"} {
		if v, ok := params[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return truncate(s, 90)
			}
		}
	}
	if len(params) == 0 {
		return ""
	}
	for k := range params {
		return truncate(k, 40)
	}
	return ""
}

// truncateAll shortens multi-line text without flattening newlines.
func truncateAll(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " \t\n") + "…"
}
