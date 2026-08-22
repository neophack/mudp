package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// feishuWebhookHostPrefix is the only webhook origin accepted for a user's
// personal notifications. Locking it down prevents turning the notification
// feature into an SSRF probe for arbitrary URLs.
const feishuWebhookHostPrefix = "https://open.feishu.cn/open-apis/bot/v2/hook/"

const feishuSendTimeout = 5 * time.Second

// normalizeFeishuWebhook validates and trims a Feishu custom-bot webhook URL.
// Empty input yields "" (clearing the setting is always allowed).
func normalizeFeishuWebhook(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	if !strings.HasPrefix(url, feishuWebhookHostPrefix) {
		return ""
	}
	return url
}

// sendFeishuText posts a plain-text message to a Feishu custom bot. Errors are
// returned so callers (the test button, the process watcher) can surface them.
func sendFeishuText(webhook, text string) error {
	payload, err := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), feishuSendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	// Feishu answers 200 with an error body (e.g. invalid webhook secret).
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err == nil && result.Code != 0 {
		return fmt.Errorf("code %d: %s", result.Code, result.Msg)
	}
	return nil
}
