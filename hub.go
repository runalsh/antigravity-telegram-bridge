package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hub is the running Antigravity hub that owns the VS Code chats. We discover
// it via /proc because port and CSRF token are random per launch and appear on
// the command line:
//
//	agy --hub --hub-port=46331 --app_data_dir=antigravity --csrf_token=<tok> --add-dir=/root
type Hub struct {
	PID        int
	Port       string
	CSRF       string
	AppDataDir string
}

var (
	reHubPort = regexp.MustCompile(`--hub-port=(\d+)`)
	reCSRF    = regexp.MustCompile(`--csrf_token=([0-9a-zA-Z\-]+)`)
	reAppDir  = regexp.MustCompile(`--app_data_dir=([^\s]+)`)
)

func findHub() (*Hub, error) {
	paths, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) == 0 {
			continue
		}
		cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
		if !strings.Contains(cmdline, "--hub-port=") {
			continue
		}
		pm, cm := reHubPort.FindStringSubmatch(cmdline), reCSRF.FindStringSubmatch(cmdline)
		if pm == nil || cm == nil {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(p)))
		h := &Hub{PID: pid, Port: pm[1], CSRF: cm[1]}
		if am := reAppDir.FindStringSubmatch(cmdline); am != nil {
			h.AppDataDir = am[1]
		}
		return h, nil
	}
	return nil, fmt.Errorf("agy hub not found — is the Antigravity extension or `agy` running?")
}

func (h *Hub) Base() string { return "http://127.0.0.1:" + h.Port }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return err == nil
}

// hubCache avoids rescanning /proc for every request.
type hubCache struct {
	mu   sync.Mutex
	hub  *Hub
	when time.Time
}

func (c *hubCache) get() (*Hub, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hub != nil && time.Since(c.when) < 10*time.Second && pidAlive(c.hub.PID) {
		return c.hub, nil
	}
	h, err := findHub()
	if err != nil {
		return nil, err
	}
	c.hub, c.when = h, time.Now()
	return h, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
