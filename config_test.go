package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg == nil {
		t.Fatal("expected non-nil default config")
	}
	if cfg.APIBase != "https://api.telegram.org" {
		t.Errorf("unexpected APIBase: %s", cfg.APIBase)
	}
	if cfg.Driver != "hub" {
		t.Errorf("unexpected Driver: %s", cfg.Driver)
	}
	if cfg.CDPURL != "http://127.0.0.1:9222" {
		t.Errorf("unexpected CDPURL: %s", cfg.CDPURL)
	}
	if !cfg.Stream {
		t.Errorf("expected Stream to be true")
	}
}

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test_config.json")

	content := `{
		"bot_token": "123456:ABC-DEF",
		"default_chat": 999888,
		"allowed_chats": [999888, 111222],
		"driver": "cdp",
		"mode": "accept-edits"
	}`

	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.BotToken != "123456:ABC-DEF" {
		t.Errorf("expected bot_token '123456:ABC-DEF', got %s", cfg.BotToken)
	}
	if cfg.DefaultChat != 999888 {
		t.Errorf("expected default_chat 999888, got %d", cfg.DefaultChat)
	}
	if cfg.Driver != "cdp" {
		t.Errorf("expected driver 'cdp', got %s", cfg.Driver)
	}
	if cfg.Mode != "accept-edits" {
		t.Errorf("expected mode 'accept-edits', got %s", cfg.Mode)
	}
	if !cfg.allowed(999888) || !cfg.allowed(111222) {
		t.Errorf("expected chats 999888 and 111222 to be allowed")
	}
	if cfg.allowed(333444) {
		t.Errorf("chat 333444 should not be allowed")
	}
}

func TestConfigValidation(t *testing.T) {
	tmpDir := t.TempDir()
	emptyPath := filepath.Join(tmpDir, "empty.json")
	if err := os.WriteFile(emptyPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(emptyPath)
	if err == nil {
		t.Errorf("expected error when bot_token is empty")
	}

	noChatPath := filepath.Join(tmpDir, "no_chat.json")
	if err := os.WriteFile(noChatPath, []byte(`{"bot_token": "token123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadConfig(noChatPath)
	if err == nil {
		t.Errorf("expected error when default_chat is missing")
	}
}

func TestConfigEnvOverrides(t *testing.T) {
	t.Setenv("TGBRIDGE_BOT_TOKEN", "env-token-777")
	t.Setenv("TGBRIDGE_CHAT", "555666")
	t.Setenv("TGBRIDGE_API_BASE", "https://proxy.example.com")

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig with env failed: %v", err)
	}

	if cfg.BotToken != "env-token-777" {
		t.Errorf("expected token from env, got %s", cfg.BotToken)
	}
	if cfg.DefaultChat != 555666 {
		t.Errorf("expected chat 555666, got %d", cfg.DefaultChat)
	}
	if cfg.APIBase != "https://proxy.example.com" {
		t.Errorf("expected APIBase 'https://proxy.example.com', got %s", cfg.APIBase)
	}
}
