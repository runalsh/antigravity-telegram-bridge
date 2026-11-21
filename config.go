package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the whole runtime configuration, loaded from a JSON file.
// Every field can be overridden by a TGBRIDGE_* environment variable.
type Config struct {
	BotToken     string  `json:"bot_token"`
	APIBase      string  `json:"api_base"`
	AllowedChats []int64 `json:"allowed_chats"`
	DefaultChat  int64   `json:"default_chat"`

	AgyBin     string `json:"agy_bin"`
	AppDataDir string `json:"app_data_dir"`
	Workdir    string `json:"workdir"`
	Mode       string `json:"mode"`
	Model      string `json:"model"`
	SkipPerms  bool   `json:"skip_permissions"`

	Stream         bool `json:"stream"`
	StreamEditMs   int  `json:"stream_edit_interval_ms"`
	ToolProgress   bool `json:"tool_progress"`
	WorkerIdleSec  int  `json:"worker_idle_seconds"`
	TurnTimeoutSec int  `json:"turn_timeout_seconds"`

	StatePath string `json:"state_path"`
	Listen    string `json:"listen"`

	WatchTranscripts bool `json:"watch_transcripts"`
	WatchIntervalMs  int  `json:"watch_interval_ms"`

	// TitlePrefix marks conversations the bridge itself created, so /ls can show them.
	TitlePrefix string `json:"title_prefix"`

	// CDP / Remote Debugging settings
	CDPURL string `json:"cdp_url"`
	Driver string `json:"driver"` // "hub" or "cdp"
}

func defaultConfig() *Config {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	return &Config{
		APIBase:          "https://api.telegram.org",
		AgyBin:           home + "/.gemini/bin/agy",
		AppDataDir:       "antigravity",
		Workdir:          home,
		Mode:             "plan",
		Stream:           true,
		StreamEditMs:     1800,
		ToolProgress:     true,
		WorkerIdleSec:    1800,
		TurnTimeoutSec:   1800,
		StatePath:        home + "/antigravity-telegram-bridge/state.json",
		Listen:           "127.0.0.1:8765",
		WatchTranscripts: true,
		WatchIntervalMs:  1500,
		TitlePrefix:      "[tg]",
		CDPURL:           "http://127.0.0.1:9222",
		Driver:           "hub",
	}
}

func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	if v := os.Getenv("TGBRIDGE_BOT_TOKEN"); v != "" {
		cfg.BotToken = v
	}
	if v := os.Getenv("TGBRIDGE_API_BASE"); v != "" {
		cfg.APIBase = v
	}
	if v := os.Getenv("TGBRIDGE_CHAT"); v != "" {
		var id int64
		if _, err := fmt.Sscanf(v, "%d", &id); err == nil {
			cfg.DefaultChat = id
			cfg.AllowedChats = append(cfg.AllowedChats, id)
		}
	}
	if v := os.Getenv("TGBRIDGE_STATE"); v != "" {
		cfg.StatePath = v
	}
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("bot_token is empty (set it in config or TGBRIDGE_BOT_TOKEN)")
	}
	if cfg.DefaultChat == 0 {
		return nil, fmt.Errorf("default_chat is not set")
	}
	if len(cfg.AllowedChats) == 0 {
		cfg.AllowedChats = []int64{cfg.DefaultChat}
	}
	if cfg.StreamEditMs < 800 {
		cfg.StreamEditMs = 1800
	}
	if cfg.WatchIntervalMs < 500 {
		cfg.WatchIntervalMs = 1500
	}
	return cfg, nil
}

func (c *Config) allowed(chat int64) bool {
	for _, id := range c.AllowedChats {
		if id == chat {
			return true
		}
	}
	return false
}

func (c *Config) geminiDir() string {
	return os.Getenv("HOME") + "/.gemini/" + c.AppDataDir
}
