package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tg is a minimal Telegram Bot API client. Base may point at a reverse proxy
// instead of api.telegram.org; the /bot<token>/<method> layout is preserved.
type Tg struct {
	token string
	base  string
	h     *http.Client
}

type TgChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	UserName string `json:"username"`
}

type TgMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Chat      TgChat `json:"chat"`
}

type TgCallbackQuery struct {
	ID      string    `json:"id"`
	From    TgChat    `json:"from"`
	Message TgMessage `json:"message"`
	Data    string    `json:"data"`
}

type TgUpdate struct {
	UpdateID      int64           `json:"update_id"`
	Message       TgMessage       `json:"message"`
	ChannelPost   TgMessage       `json:"channel_post"`
	CallbackQuery TgCallbackQuery `json:"callback_query"`
}

func (u TgUpdate) message() (TgMessage, bool) {
	if u.Message.MessageID != 0 {
		return u.Message, true
	}
	if u.ChannelPost.MessageID != 0 {
		return u.ChannelPost, true
	}
	return TgMessage{}, false
}

func NewTg(token, base string) *Tg {
	return &Tg{token: token, base: base, h: &http.Client{Timeout: 60 * time.Second}}
}

func (t *Tg) methodURL(m string) string {
	return fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(t.base, "/"), t.token, m)
}

func (t *Tg) call(m string, form url.Values) (json.RawMessage, error) {
	resp, err := t.h.PostForm(t.methodURL(m), form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: bad response %q", m, truncate(string(raw), 200))
	}
	if !parsed.OK {
		return nil, fmt.Errorf("%s: %s", m, parsed.Description)
	}
	return parsed.Result, nil
}

func (t *Tg) Ping() error {
	_, err := t.call("getMe", url.Values{})
	return err
}

func (t *Tg) GetUpdates(offset int64) ([]TgUpdate, error) {
	form := url.Values{
		"offset":          {fmt.Sprint(offset)},
		"timeout":         {"25"},
		"limit":           {"50"},
		"allowed_updates": {`["message","channel_post","callback_query"]`},
	}
	raw, err := t.call("getUpdates", form)
	if err != nil {
		return nil, err
	}
	var ups []TgUpdate
	if err := json.Unmarshal(raw, &ups); err != nil {
		return nil, err
	}
	return ups, nil
}

// Send posts a message and returns its id. parseMode is "" or "HTML".
func (t *Tg) Send(chat int64, text, parseMode string) (int64, error) {
	id, err := t.sendInner(chat, text, parseMode)
	if err != nil && parseMode != "" && strings.Contains(err.Error(), "can't parse") {
		return t.sendInner(chat, text, "")
	}
	return id, err
}

func (t *Tg) sendInner(chat int64, text, parseMode string) (int64, error) {
	form := url.Values{"chat_id": {fmt.Sprint(chat)}, "text": {text}}
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	raw, err := t.call("sendMessage", form)
	if err != nil {
		return 0, err
	}
	var msg TgMessage
	_ = json.Unmarshal(raw, &msg)
	return msg.MessageID, nil
}

// Edit replaces the text of an existing message; msgID 0 means "just send".
func (t *Tg) Edit(chat, msgID int64, text, parseMode string) error {
	if msgID == 0 {
		_, err := t.Send(chat, text, parseMode)
		return err
	}
	form := url.Values{
		"chat_id":    {fmt.Sprint(chat)},
		"message_id": {fmt.Sprint(msgID)},
		"text":       {text},
	}
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	_, err := t.call("editMessageText", form)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	if err != nil && parseMode != "" && strings.Contains(err.Error(), "can't parse") {
		form.Del("parse_mode")
		_, err = t.call("editMessageText", form)
	}
	return err
}

// SendChunked splits long text into Telegram-sized parts.
func (t *Tg) SendChunked(chat int64, text, parseMode string) error {
	for _, p := range chunkText(text, 3900) {
		if _, err := t.Send(chat, p, parseMode); err != nil {
			return err
		}
		time.Sleep(120 * time.Millisecond)
	}
	return nil
}

// SendKeyboard posts a message with an inline keyboard.
func (t *Tg) SendKeyboard(chat int64, text string, keyboard map[string]any) (int64, error) {
	kbBytes, _ := json.Marshal(keyboard)
	form := url.Values{
		"chat_id":      {fmt.Sprint(chat)},
		"text":         {text},
		"parse_mode":   {"HTML"},
		"reply_markup": {string(kbBytes)},
	}
	raw, err := t.call("sendMessage", form)
	if err != nil {
		return 0, err
	}
	var msg TgMessage
	_ = json.Unmarshal(raw, &msg)
	return msg.MessageID, nil
}

// AnswerCallbackQuery acknowledges a callback button click.
func (t *Tg) AnswerCallbackQuery(queryID, text string) error {
	form := url.Values{"callback_query_id": {queryID}}
	if text != "" {
		form.Set("text", text)
	}
	_, err := t.call("answerCallbackQuery", form)
	return err
}

