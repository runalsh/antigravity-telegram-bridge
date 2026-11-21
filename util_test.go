package main

import (
	"strings"
	"testing"
	"time"
)

func TestHTMLEscape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"hello world", "hello world"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"Tom & Jerry", "Tom &amp; Jerry"},
		{"a < b && b > c", "a &lt; b &amp;&amp; b &gt; c"},
	}

	for _, tt := range tests {
		got := HTMLEscape(tt.in)
		if got != tt.want {
			t.Errorf("HTMLEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exact10len", 10, "exact10len"},
		{"longer than ten", 10, "longer tha…"},
		{"hello\nworld", 20, "hello world"},
		{"привет мир!", 6, "привет…"},
		{"🚀🌟🔥🎉✨", 3, "🚀🌟🔥…"},
	}

	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestChunkText(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		res := chunkText("", 100)
		if len(res) != 1 || res[0] != "" {
			t.Errorf("expected [\"\"], got %#v", res)
		}
	})

	t.Run("short text under limit", func(t *testing.T) {
		res := chunkText("hello world", 100)
		if len(res) != 1 || res[0] != "hello world" {
			t.Errorf("expected single chunk, got %#v", res)
		}
	})

	t.Run("multiline under limit", func(t *testing.T) {
		text := "line 1\nline 2\nline 3"
		res := chunkText(text, 100)
		if len(res) != 1 || res[0] != text {
			t.Errorf("expected combined text, got %#v", res)
		}
	})

	t.Run("long line split", func(t *testing.T) {
		longLine := strings.Repeat("A", 120)
		res := chunkText(longLine, 50)
		if len(res) < 2 {
			t.Fatalf("expected at least 2 chunks, got %d", len(res))
		}
		totalLen := 0
		for _, c := range res {
			if len(c) > 50 {
				t.Errorf("chunk exceeds limit: len %d", len(c))
			}
			totalLen += len(c)
		}
		if totalLen != 120 {
			t.Errorf("expected total len 120, got %d", totalLen)
		}
	})
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "hello", "world"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := firstNonEmpty("", "   "); got != "" {
		t.Errorf("expected '', got %q", got)
	}
}

func TestShortDur(t *testing.T) {
	d := 65 * time.Second
	got := shortDur(d)
	if got != "1m5s" {
		t.Errorf("expected '1m5s', got %q", got)
	}
}
