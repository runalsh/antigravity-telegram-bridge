package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// CDPClient manages the Chrome DevTools Protocol connection to the Antigravity chat webview.
type CDPClient struct {
	addr      string // default "http://127.0.0.1:9222"
	mu        sync.Mutex
	conn      *websocket.Conn
	msgID     int64
	pageID    string
	targetURL string
	actionWS  string
}

func NewCDPClient(addr string) *CDPClient {
	if addr == "" {
		addr = "http://127.0.0.1:9222"
	}
	return &CDPClient{addr: addr}
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Connect finds the active Antigravity chat iframe target and dials its WebSocket.
func (c *CDPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	resp, err := http.Get(c.addr + "/json/list")
	if err != nil {
		return fmt.Errorf("cdp endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return err
	}

	var best *cdpTarget
	// Priority 1: direct webview instance with active conversation URL
	for _, t := range targets {
		if strings.Contains(t.URL, "extensionVariant=vs-code") || (strings.Contains(t.URL, "/c/") && strings.Contains(t.URL, "36196")) {
			best = &t
			break
		}
	}
	// Priority 2: webview with google.google-antigravity
	if best == nil {
		for _, t := range targets {
			if strings.Contains(t.URL, "extensionId=google.google-antigravity") || strings.Contains(t.Title, "Antigravity") {
				best = &t
				break
			}
		}
	}

	if best == nil {
		return fmt.Errorf("no Antigravity chat webview found in CDP targets")
	}

	conn, _, err := websocket.DefaultDialer.Dial(best.WebSocketDebuggerURL, nil)
	if err != nil {
		return fmt.Errorf("dial cdp ws failed: %w", err)
	}

	c.conn = conn
	c.pageID = best.ID
	c.targetURL = best.URL
	logf("cdp connected to %s (%s)", best.Title, best.ID)
	return nil
}

// Evaluate evaluates a JS expression in the webview and parses the JSON result.
func (c *CDPClient) Evaluate(ctx context.Context, expression string, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("cdp not connected")
	}

	c.msgID++
	id := c.msgID

	req := map[string]any{
		"id":     id,
		"method": "Runtime.evaluate",
		"params": map[string]any{
			"expression":    expression,
			"returnByValue": true,
			"awaitPromise":  true,
		},
	}

	if err := c.conn.WriteJSON(req); err != nil {
		c.conn.Close()
		c.conn = nil
		return fmt.Errorf("write error: %w", err)
	}

	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var raw map[string]any
		if err := c.conn.ReadJSON(&raw); err != nil {
			c.conn.Close()
			c.conn = nil
			return fmt.Errorf("read error: %w", err)
		}

		if rawID, ok := raw["id"].(float64); ok && int64(rawID) == id {
			if errObj, ok := raw["error"]; ok {
				return fmt.Errorf("cdp error: %v", errObj)
			}
			res, ok := raw["result"].(map[string]any)
			if !ok {
				return fmt.Errorf("malformed result: %v", raw)
			}
			resVal, ok := res["result"].(map[string]any)
			if !ok {
				return nil
			}
			val := resVal["value"]
			if out == nil || val == nil {
				return nil
			}
			b, err := json.Marshal(val)
			if err != nil {
				return err
			}
			return json.Unmarshal(b, out)
		}
	}
}

// SendPrompt injects text into the chat input and simulates pressing Enter.
func (c *CDPClient) SendPrompt(ctx context.Context, text string) error {
	js := fmt.Sprintf(`(() => {
		const text = %s;
		const editor = document.querySelector('div[contenteditable="true"]');
		if (!editor) return { error: "Chat input (contenteditable) not found" };

		// Focus and select all
		editor.focus();
		const selection = window.getSelection();
		const range = document.createRange();
		range.selectNodeContents(editor);
		selection.removeAllRanges();
		selection.addRange(range);

		// Insert text using execCommand so ProseMirror/Draft.js registers state change
		const inserted = document.execCommand('insertText', false, text);
		if (!inserted) {
			editor.innerText = text;
			editor.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text }));
		}

		// Look for Send button
		const sendBtn = document.querySelector('button[aria-label*="Send"], button[title*="Send"], button:has(svg path[d*="M2.01 21L23 12 2.01 3"])');
		if (sendBtn && !sendBtn.disabled) {
			sendBtn.click();
			return { ok: true, method: "button_click" };
		}

		// Fallback: Dispatch Enter Key
		const enterDown = new KeyboardEvent('keydown', {
			key: 'Enter',
			code: 'Enter',
			keyCode: 13,
			which: 13,
			bubbles: true,
			cancelable: true
		});
		editor.dispatchEvent(enterDown);

		const enterUp = new KeyboardEvent('keyup', {
			key: 'Enter',
			code: 'Enter',
			keyCode: 13,
			which: 13,
			bubbles: true,
			cancelable: true
		});
		editor.dispatchEvent(enterUp);

		return { ok: true, method: "enter_dispatched" };
	})()`, mustJSON(text))

	var res map[string]any
	if err := c.Evaluate(ctx, js, &res); err != nil {
		return err
	}
	if errMsg, ok := res["error"].(string); ok {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

// ActionButton represents an interactive choice or approval button.
type ActionButton struct {
	Index  int    `json:"index"`
	Action string `json:"action"`
	Text   string `json:"text"`
}

// ActionPrompt represents pending user actions (ask_question, permission approvals).
type ActionPrompt struct {
	Question string         `json:"question"`
	Command  string         `json:"command"`
	Buttons  []ActionButton `json:"buttons"`
}

// DetectPendingActions scans ALL relevant VS Code targets (chat webview, outer webview, workbench)
func (c *CDPClient) DetectPendingActions(ctx context.Context) (*ActionPrompt, error) {
	c.mu.Lock()
	endpoint := c.addr + "/json/list"
	c.mu.Unlock()

	resp, err := http.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, err
	}

	js := `(() => {
		const normalize = (t) => (t || '').toLowerCase().replace(/\s+/g, ' ').trim();

		// Clean old tags
		document.querySelectorAll('[data-tgbridge-action]').forEach(el => el.removeAttribute('data-tgbridge-action'));

		// Find active permission prompt in document, strictly outside chat history and visible on screen
		const allEls = Array.from(document.querySelectorAll('button, [role="button"], [role="radio"], div, span')).filter(el => {
			if (el.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]')) return false;
			const rect = el.getBoundingClientRect();
			return rect.top >= 0 && rect.height > 0;
		}).reverse();

		const allowOnce = allEls.find(el => {
			const t = normalize(el.innerText || '');
			return t.includes('yes, allow this time') || t === 'allow once' || t.startsWith('1 yes');
		});

		if (allowOnce) {
			// Locate exact card without ever climbing to ancestors that contain autoscroll-viewport
			let container = allowOnce.parentElement;
			while (container && container !== document.body && container.parentElement) {
				if (container.parentElement.querySelector('[data-testid="autoscroll-viewport"]')) {
					// Do not climb to parent if parent contains the chat viewport
					break;
				}
				if (container.querySelector('button') && (container.innerText || '').toLowerCase().includes('submit')) {
					break;
				}
				container = container.parentElement;
			}
			if (!container || container === document.body) {
				container = allowOnce.parentElement?.parentElement || allowOnce.parentElement;
			}

			// Extract question & command from container
			let question = "Confirmation required in VS Code";
			const titleMatch = (container.innerText || "").match(/Allow\s+[^?\n]+\?/i);
			if (titleMatch) {
				question = titleMatch[0];
			} else {
				const h = container.querySelector('h1, h2, h3, h4, [class*="font-semibold"], [class*="title"]');
				if (h && (h.innerText || '').trim()) {
					question = h.innerText.trim();
				}
			}

			let cmd = "";
			const codeEls = Array.from(container.querySelectorAll('pre, code, [class*="font-mono"]')).filter(el => {
				if (el.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]')) return false;
				const t = (el.innerText || '').trim();
				return t.length > 2 && !/^\d+$/.test(t);
			});
			if (codeEls.length > 0) {
				cmd = codeEls[0].innerText.trim();
			}

			// Collect ONLY specific action buttons
			const buttons = [];
			const seen = new Set();

			const addBtn = (el, action, label) => {
				if (!el) return;
				const text = label || (el.innerText || '').trim().replace(/\s+/g, ' ');
				if (seen.has(action)) return;
				seen.add(action);
				el.setAttribute('data-tgbridge-action', action);
				buttons.push({ index: buttons.length, action: action, text: text });
			};

			const containerEls = Array.from(container.querySelectorAll('button, [role="button"], [role="radio"], div, span'));

			// 1. Yes, allow this time
			addBtn(allowOnce, "allow_once", "1 Yes, allow this time");

			// 2. Yes, and always allow in this conversation
			const alwaysConv = containerEls.find(el => {
				const t = normalize(el.innerText || '');
				return t.includes('always allow') && t.includes('conversation');
			});
			if (alwaysConv) addBtn(alwaysConv, "allow_conversation", "2 Yes, and always allow in this conversation");

			// 3. Yes, and always allow
			const alwaysAll = containerEls.find(el => {
				const t = normalize(el.innerText || '');
				return t.includes('always allow') && !t.includes('conversation');
			});
			if (alwaysAll) addBtn(alwaysAll, "always_allow", "3 Yes, and always allow");

			// 4. No
			const denyBtn = containerEls.find(el => {
				const t = normalize(el.innerText || '');
				return t.startsWith('4 no') || t === 'no' || t.includes('tell the agent');
			});
			if (denyBtn) addBtn(denyBtn, "deny", "4 No");

			// 5. Skip
			const skipBtn = containerEls.find(el => normalize(el.innerText || '') === 'skip');
			if (skipBtn) addBtn(skipBtn, "skip", "Skip");

			// 6. Submit
			const submitBtn = containerEls.find(el => {
				const t = normalize(el.innerText || '');
				const a = normalize(el.getAttribute('aria-label') || '');
				return (t.includes('submit') || a.includes('submit')) && !t.includes('skip') && !t.includes('no');
			});
			if (submitBtn) addBtn(submitBtn, "submit", "Submit ↵");

			return { question: question, command: cmd, buttons: buttons };
		}

		return { question: "", command: "", buttons: [] };
	})()`

	for _, t := range targets {
		if t.WebSocketDebuggerURL == "" {
			continue
		}
		// Only inspect Antigravity chat webviews, never workbench or other pages
		if !strings.Contains(t.URL, "extensionVariant=vs-code") && !strings.Contains(t.URL, "36196") && !strings.Contains(t.URL, "google-antigravity") {
			continue
		}

		conn, _, err := websocket.DefaultDialer.Dial(t.WebSocketDebuggerURL, nil)
		if err != nil {
			continue
		}

		req := map[string]any{
			"id":     9999,
			"method": "Runtime.evaluate",
			"params": map[string]any{
				"expression":    js,
				"returnByValue": true,
			},
		}
		_ = conn.WriteJSON(req)
		conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		var msg map[string]any
		_ = conn.ReadJSON(&msg)
		conn.Close()

		if res, ok := msg["result"].(map[string]any); ok {
			if resVal, ok := res["result"].(map[string]any); ok {
				if val := resVal["value"]; val != nil {
					b, _ := json.Marshal(val)
					var p ActionPrompt
					if err := json.Unmarshal(b, &p); err == nil && len(p.Buttons) > 0 {
						c.mu.Lock()
						c.pageID = t.ID
						c.targetURL = t.URL
						c.actionWS = t.WebSocketDebuggerURL
						c.mu.Unlock()
						return &p, nil
					}
				}
			}
		}
	}

	return &ActionPrompt{}, nil
}

// ClickAction clicks an action by explicit action name (allow_once, allow_conversation, always_allow, deny, skip, submit).
func (c *CDPClient) ClickAction(ctx context.Context, action string) error {
	c.mu.Lock()
	wsURL := c.actionWS
	c.mu.Unlock()

	js := fmt.Sprintf(`new Promise(resolve => {
		const action = %q;
		const composer = document.querySelector('.surface-composer') || document.querySelector('form');
		const composerArea = composer ? composer.parentElement : document.body;

		const clickEl = (el) => {
			if (!el) return false;
			el.focus();
			for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) {
				el.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, view: window }));
			}
			if (typeof el.click === 'function') el.click();
			return true;
		};

		const findSubmit = () => {
			// 1. Look inside the exact card containing the selected option
			if (optEl) {
				let card = optEl.parentElement;
				while (card && card !== document.body && card.parentElement) {
					if (card.parentElement.querySelector('[data-testid="autoscroll-viewport"]')) {
						break;
					}
					if (card.querySelector('button') && (card.innerText || '').toLowerCase().includes('submit')) {
						break;
					}
					card = card.parentElement;
				}
				if (card) {
					const btns = Array.from(card.querySelectorAll('button, [role="button"]')).reverse();
					const sub = btns.find(b => {
						const t = (b.innerText || '').toLowerCase().trim();
						const a = (b.getAttribute('aria-label') || '').toLowerCase().trim();
						return (t.includes('submit') || a.includes('submit')) && !t.includes('skip');
					});
					if (sub) return sub;
				}
			}

			// 2. Look for strictly visible button on screen (top >= 0, height > 0)
			const visibleBtns = Array.from(document.querySelectorAll('button, [role="button"]')).filter(b => {
				if (b.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]')) return false;
				const rect = b.getBoundingClientRect();
				return rect.top >= 0 && rect.height > 0;
			}).reverse();

			return visibleBtns.find(b => {
				const t = (b.innerText || '').toLowerCase().trim();
				const a = (b.getAttribute('aria-label') || '').toLowerCase().trim();
				return (t.includes('submit') || a.includes('submit')) && !t.includes('skip');
			});
		};

		if (action === "skip") {
			const tagged = document.querySelector('[data-tgbridge-action="skip"]');
			if (tagged) {
				clickEl(tagged);
				resolve({ ok: true, method: "skip_tagged_clicked" });
				return;
			}
			const btns = Array.from(document.querySelectorAll('button, [role="button"], span, div')).filter(b => {
				return !b.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]');
			});
			const skipBtn = btns.find(b => {
				const t = (b.innerText || '').toLowerCase().trim();
				return t === 'skip';
			});
			if (skipBtn) {
				clickEl(skipBtn);
				resolve({ ok: true, method: "skip_clicked" });
				return;
			}
		}

		if (action === "deny") {
			// Try skip button first for clean 1-click rejection
			const tagged = document.querySelector('[data-tgbridge-action="skip"]');
			if (tagged) {
				clickEl(tagged);
				resolve({ ok: true, method: "skip_tagged_clicked" });
				return;
			}
			const btns = Array.from(document.querySelectorAll('button, [role="button"], span, div')).filter(b => {
				return !b.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]');
			});
			const skipBtn = btns.find(b => {
				const t = (b.innerText || '').toLowerCase().trim();
				return t === 'skip';
			});
			if (skipBtn) {
				clickEl(skipBtn);
				resolve({ ok: true, method: "skip_clicked_for_deny" });
				return;
			}
			// Fallback: fill textarea and submit
			const ta = document.querySelector('textarea');
			if (ta) {
				ta.value = "No";
				ta.dispatchEvent(new Event('input', { bubbles: true }));
			}
			setTimeout(() => {
				const sub = findSubmit();
				clickEl(sub);
				resolve({ ok: true, method: "no_submitted" });
			}, 200);
			return;
		}

		if (action === "submit") {
			const sub = findSubmit();
			if (sub) {
				clickEl(sub);
				resolve({ ok: true, method: "submit_clicked" });
			} else {
				resolve({ error: "Submit button not found" });
			}
			return;
		}

		// Option selection: allow_once (1), allow_conversation (2), always_allow (3)
		const tagged = Array.from(document.querySelectorAll('[data-tgbridge-action="' + action + '"]')).reverse().find(el => {
			const rect = el.getBoundingClientRect();
			return rect.top >= 0 && rect.height > 0;
		});
		let optEl = tagged;

		if (!optEl) {
			const allEls = Array.from(document.querySelectorAll('*')).filter(el => {
				if (el.closest('[data-testid="autoscroll-viewport"], [data-quotable-root], [data-markdown-root]')) return false;
				const rect = el.getBoundingClientRect();
				return rect.top >= 0 && rect.height > 0;
			}).reverse();

			if (action === "allow_once" || action === "0") {
				optEl = allEls.find(el => {
					const t = (el.innerText || '').toLowerCase().trim();
					return t.includes('yes, allow this time') || t.startsWith('1 yes');
				});
			} else if (action === "allow_conversation" || action === "1") {
				optEl = allEls.find(el => {
					const t = (el.innerText || '').toLowerCase().trim();
					return t.includes('always allow') && t.includes('conversation');
				});
			} else if (action === "always_allow" || action === "2" || action === "3" || action === "4") {
				optEl = allEls.find(el => {
					const t = (el.innerText || '').toLowerCase().trim();
					return t.includes('always allow') && !t.includes('conversation');
				});
			}
		}

		if (!optEl) {
			optEl = composerArea.querySelector('[data-tgbridge-action="' + action + '"]') || document.querySelector('[data-tgbridge-action="' + action + '"]');
		}

		if (!optEl) {
			resolve({ error: "Option element not found for action: " + action });
			return;
		}

		// Ensure we click row and the inner radio
		const row = optEl.closest('button, [role="button"], [role="radio"], [role="option"], label, div[class*="cursor-pointer"], div[tabindex]') || optEl;
		clickEl(row);
		if (row !== optEl) clickEl(optEl);

		// Auto-submit after selection with retries
		let attempts = 0;
		const trySubmit = () => {
			attempts++;
			const sub = findSubmit();
			if (sub && !sub.disabled) {
				const rect = sub.getBoundingClientRect();
				clickEl(sub);
				const enterDown = new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true });
				sub.dispatchEvent(enterDown);
				const enterUp = new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true });
				sub.dispatchEvent(enterUp);
				resolve({
					ok: true,
					action: action,
					submitted: true,
					x: rect.left + rect.width / 2,
					y: rect.top + rect.height / 2
				});
			} else if (attempts < 8) {
				setTimeout(trySubmit, 150);
			} else {
				resolve({ ok: true, action: action, submitNotFound: !sub, disabled: sub ? sub.disabled : false });
			}
		};
		setTimeout(trySubmit, 250);
	})`, action)

	if wsURL != "" {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			defer conn.Close()
			req := map[string]any{
				"id":     9998,
				"method": "Runtime.evaluate",
				"params": map[string]any{
					"expression":    js,
					"returnByValue": true,
					"awaitPromise":  true,
				},
			}
			_ = conn.WriteJSON(req)
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err == nil {
				if res, ok := msg["result"].(map[string]any); ok {
					if resVal, ok := res["result"].(map[string]any); ok {
						if val, ok := resVal["value"].(map[string]any); ok {
							logf("ClickAction act=%s res=%+v", action, val)
							if errMsg, ok := val["error"].(string); ok {
								return fmt.Errorf("%s", errMsg)
							}
							if x, okX := val["x"].(float64); okX {
								if y, okY := val["y"].(float64); okY && x > 0 && y > 0 {
									// Native hardware mouse click via CDP
									_ = conn.WriteJSON(map[string]any{
										"id": 9991, "method": "Input.dispatchMouseEvent",
										"params": map[string]any{"type": "mouseMoved", "x": x, "y": y},
									})
									_ = conn.WriteJSON(map[string]any{
										"id": 9992, "method": "Input.dispatchMouseEvent",
										"params": map[string]any{"type": "mousePressed", "button": "left", "clickCount": 1, "x": x, "y": y},
									})
									_ = conn.WriteJSON(map[string]any{
										"id": 9993, "method": "Input.dispatchMouseEvent",
										"params": map[string]any{"type": "mouseReleased", "button": "left", "clickCount": 1, "x": x, "y": y},
									})
									// Enter key via CDP
									_ = conn.WriteJSON(map[string]any{
										"id": 9994, "method": "Input.dispatchKeyEvent",
										"params": map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "nativeVirtualKeyCode": 13},
									})
									_ = conn.WriteJSON(map[string]any{
										"id": 9995, "method": "Input.dispatchKeyEvent",
										"params": map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "nativeVirtualKeyCode": 13},
									})
								}
							}
							return nil
						}
					}
				}
			}
		}
	}

	var res map[string]any
	if err := c.Evaluate(ctx, js, &res); err != nil {
		return err
	}
	logf("ClickAction fallback act=%s res=%+v", action, res)
	if errMsg, ok := res["error"].(string); ok {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

// AnswerQuestion answers an active ask_question card in the webview by clicking the option and submitting.
func (c *CDPClient) AnswerQuestion(ctx context.Context, choice string, index int) error {
	js := fmt.Sprintf(`new Promise(resolve => {
		const choiceText = %q;
		const index = %d;

		const clickEl = (el) => {
			if (!el) return false;
			el.focus();
			for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) {
				el.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, view: window }));
			}
			if (typeof el.click === 'function') el.click();
			return true;
		};

		// Look for active question form or card
		const forms = Array.from(document.querySelectorAll('form, [role="form"], div[class*="rounded"]'));
		const activeForm = forms.reverse().find(f => {
			const radios = f.querySelectorAll('[role="radio"], [role="option"], label, input[type="radio"]');
			const btns = Array.from(f.querySelectorAll('button')).filter(b => (b.innerText || '').toLowerCase().includes('submit'));
			return radios.length > 0 && btns.length > 0;
		});

		if (!activeForm) {
			resolve({ error: "no active question form found" });
			return;
		}

		const radios = Array.from(activeForm.querySelectorAll('[role="radio"], [role="option"], label, input[type="radio"]'));
		let target = radios.find(r => (r.innerText || r.textContent || '').toLowerCase().includes(choiceText.toLowerCase()));
		if (!target && index >= 0 && index < radios.length) {
			target = radios[index];
		}

		if (!target) {
			resolve({ error: "radio option not found" });
			return;
		}

		clickEl(target);

		let attempts = 0;
		const trySubmit = () => {
			attempts++;
			const sub = Array.from(activeForm.querySelectorAll('button')).find(b => {
				const t = (b.innerText || '').toLowerCase();
				return t.includes('submit') || t.includes('confirm');
			});
			if (sub && !sub.disabled) {
				clickEl(sub);
				resolve({ ok: true, submitted: true });
			} else if (attempts < 6) {
				setTimeout(trySubmit, 150);
			} else {
				resolve({ ok: true, submitted: false, disabled: sub ? sub.disabled : false });
			}
		};
		setTimeout(trySubmit, 150);
	})`, choice, index)

	var res map[string]any
	if err := c.Evaluate(ctx, js, &res); err != nil {
		return err
	}
	if errMsg, ok := res["error"].(string); ok {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

// ConversationInfo holds conversation details parsed from the UI sidebar.
type ConversationInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	IsActive bool   `json:"isActive"`
}

// ListConversations scans the UI for conversations in the sidebar.
func (c *CDPClient) ListConversations(ctx context.Context) ([]ConversationInfo, error) {
	js := `(() => {
		const list = [];
		// Look for sidebar items or conversation buttons
		const delButtons = document.querySelectorAll('[data-tooltip-id$="-delete-conversation"]');
		if (delButtons.length > 0) {
			delButtons.forEach(delBtn => {
				const tooltip = delBtn.getAttribute('data-tooltip-id') || '';
				const convId = tooltip.replace('-delete-conversation', '');
				const parent = delBtn.closest('button, div[role="button"]') || delBtn.parentElement;
				const title = parent ? (parent.innerText || convId).trim() : convId;
				list.push({ id: convId, title: title, isActive: false });
			});
			return list;
		}

		// Fallback: query active conversation title from header
		const title = document.title || "Current Conversation";
		const activeID = window.location.pathname.split('/').pop() || "";
		return [{ id: activeID, title: title, isActive: true }];
	})()`

	var list []ConversationInfo
	if err := c.Evaluate(ctx, js, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
