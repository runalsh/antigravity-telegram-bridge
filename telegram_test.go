package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTgUpdateMessage(t *testing.T) {
	// 1. Direct message
	u1 := TgUpdate{
		Message: TgMessage{MessageID: 101, Text: "hi"},
	}
	m1, ok1 := u1.message()
	if !ok1 || m1.MessageID != 101 || m1.Text != "hi" {
		t.Errorf("expected direct message, got %#v", m1)
	}

	// 2. Channel post
	u2 := TgUpdate{
		ChannelPost: TgMessage{MessageID: 202, Text: "broadcast"},
	}
	m2, ok2 := u2.message()
	if !ok2 || m2.MessageID != 202 || m2.Text != "broadcast" {
		t.Errorf("expected channel post, got %#v", m2)
	}

	// 3. Callback only
	u3 := TgUpdate{
		CallbackQuery: TgCallbackQuery{ID: "cb-1"},
	}
	_, ok3 := u3.message()
	if ok3 {
		t.Errorf("expected false for callback-only update")
	}
}

func TestTgMethodURL(t *testing.T) {
	tg := NewTg("token123", "https://api.telegram.org/")
	url := tg.methodURL("sendMessage")
	if url != "https://api.telegram.org/bottoken123/sendMessage" {
		t.Errorf("unexpected methodURL: %s", url)
	}

	tg2 := NewTg("token456", "https://custom.proxy.dev")
	url2 := tg2.methodURL("getMe")
	if url2 != "https://custom.proxy.dev/bottoken456/getMe" {
		t.Errorf("unexpected methodURL: %s", url2)
	}
}

func TestTgSendAndEdit(t *testing.T) {
	var lastMethod string
	var lastChatID string
	var lastText string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		lastMethod = parts[len(parts)-1]
		_ = r.ParseForm()
		lastChatID = r.Form.Get("chat_id")
		lastText = r.Form.Get("text")

		resp := map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 999,
				"text":       lastText,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	tg := NewTg("mock-token", mockServer.URL)

	// Test Send
	msgID, err := tg.Send(777, "Hello Telegram", "HTML")
	if err != nil {
		t.Fatalf("tg.Send failed: %v", err)
	}
	if msgID != 999 {
		t.Errorf("expected message_id 999, got %d", msgID)
	}
	if lastMethod != "sendMessage" || lastChatID != "777" || lastText != "Hello Telegram" {
		t.Errorf("unexpected call: method=%s chat=%s text=%s", lastMethod, lastChatID, lastText)
	}

	// Test Edit
	err = tg.Edit(777, 999, "Edited Text", "")
	if err != nil {
		t.Fatalf("tg.Edit failed: %v", err)
	}
	if lastMethod != "editMessageText" || lastText != "Edited Text" {
		t.Errorf("unexpected edit call: method=%s text=%s", lastMethod, lastText)
	}

	// Test SendKeyboard
	keyboard := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{{"text": "Btn 1", "callback_data": "data1"}},
		},
	}
	kMsgID, err := tg.SendKeyboard(777, "Choose:", keyboard)
	if err != nil {
		t.Fatalf("tg.SendKeyboard failed: %v", err)
	}
	if kMsgID != 999 {
		t.Errorf("expected kMsgID 999, got %d", kMsgID)
	}
	if lastMethod != "sendMessage" {
		t.Errorf("expected sendMessage, got %s", lastMethod)
	}
}
