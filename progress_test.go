package main

import (
	"testing"
)

func TestTruncateAll(t *testing.T) {
	text := "line 1\nline 2\nline 3"
	got := truncateAll(text, 10)
	expected := "line 1\nlin…"
	if got != expected {
		t.Errorf("truncateAll(%q, 10) = %q, want %q", text, got, expected)
	}

	short := "short"
	if truncateAll(short, 10) != short {
		t.Errorf("expected %q, got %q", short, truncateAll(short, 10))
	}
}

func TestToolDetail(t *testing.T) {
	tests := []struct {
		params map[string]any
		want   string
	}{
		{
			params: map[string]any{"CommandLine": "git status -s"},
			want:   "git status -s",
		},
		{
			params: map[string]any{"Path": "/root/project/main.go"},
			want:   "/root/project/main.go",
		},
		{
			params: map[string]any{"query": "search query"},
			want:   "search query",
		},
		{
			params: map[string]any{"other_key": "some value"},
			want:   "other_key",
		},
		{
			params: map[string]any{},
			want:   "",
		},
	}

	for _, tt := range tests {
		got := toolDetail(tt.params)
		if got != tt.want {
			t.Errorf("toolDetail(%+v) = %q, want %q", tt.params, got, tt.want)
		}
	}
}

func TestProgressLineFormatting(t *testing.T) {
	p := newProgress(nil, 1234, true)
	if line := p.progressLine(); line != "🔧 …thinking" {
		t.Errorf("expected '🔧 …thinking', got %q", line)
	}

	p.tools = append(p.tools, "run_command ls -la")
	if line := p.progressLine(); line != "🔧 • run_command ls -la" {
		t.Errorf("expected '🔧 • run_command ls -la', got %q", line)
	}

	p.text.WriteString("Working on it...")
	expected := "🤖 Working on it...\n\n🔧 • run_command ls -la"
	if line := p.progressLine(); line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}
