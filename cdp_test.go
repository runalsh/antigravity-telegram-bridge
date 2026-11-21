package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

func TestCDPClientConnectAndEvaluate(t *testing.T) {
	// Mock WebSocket handler for CDP
	wsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}

			// Respond with mock CDP Runtime.evaluate response
			resp := map[string]any{
				"id": req["id"],
				"result": map[string]any{
					"result": map[string]any{
						"type": "object",
						"value": map[string]any{
							"status": "ok",
							"count":  42,
						},
					},
				},
			}
			raw, _ := json.Marshal(resp)
			conn.WriteMessage(websocket.TextMessage, raw)
		}
	})

	wsServer := httptest.NewServer(wsHandler)
	defer wsServer.Close()
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	// Mock CDP HTTP server
	mockCDP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			targets := []cdpTarget{
				{
					ID:                   "workbench-1",
					Title:                "Visual Studio Code",
					Type:                 "page",
					URL:                  "vscode-file://vscode-app/workbench.html",
					WebSocketDebuggerURL: wsURL + "/workbench",
				},
				{
					ID:                   "antigravity-chat-1",
					Title:                "Antigravity Chat",
					Type:                 "iframe",
					URL:                  "http://127.0.0.1:36196/c/91098d93-f9e9?extensionVariant=vs-code",
					WebSocketDebuggerURL: wsURL,
				},
			}
			json.NewEncoder(w).Encode(targets)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockCDP.Close()

	client := NewCDPClient(mockCDP.URL)
	if err := client.Connect(); err != nil {
		t.Fatalf("CDPClient.Connect failed: %v", err)
	}

	if client.pageID != "antigravity-chat-1" {
		t.Errorf("expected target 'antigravity-chat-1', got %s", client.pageID)
	}

	var evalResult struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}

	err := client.Evaluate(context.Background(), "document.title", &evalResult)
	if err != nil {
		t.Fatalf("CDPClient.Evaluate failed: %v", err)
	}

	if evalResult.Status != "ok" || evalResult.Count != 42 {
		t.Errorf("unexpected evaluate result: %#v", evalResult)
	}
}

func TestCDPClientNoTargets(t *testing.T) {
	mockEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			json.NewEncoder(w).Encode([]cdpTarget{})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockEmpty.Close()

	client := NewCDPClient(mockEmpty.URL)
	err := client.Connect()
	if err == nil {
		t.Errorf("expected error when no targets are found")
	}
}
