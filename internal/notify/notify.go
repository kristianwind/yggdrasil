// Package notify delivers event notifications to Telegram, Discord, or a generic
// webhook. Channel credentials are encrypted at rest by the caller.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Config is a decrypted notification channel.
type Config struct {
	Type   string `json:"type"`              // telegram | discord | webhook
	Token  string `json:"token,omitempty"`   // telegram bot token
	ChatID string `json:"chat_id,omitempty"` // telegram chat id
	URL    string `json:"url,omitempty"`     // discord/webhook URL
}

var client = &http.Client{Timeout: 10 * time.Second}

// Send delivers text to the channel described by cfg.
func Send(cfg Config, text string) error {
	switch cfg.Type {
	case "telegram":
		return sendTelegram(cfg, text)
	case "discord":
		return sendDiscord(cfg, text)
	case "webhook":
		return sendWebhook(cfg, text)
	default:
		return fmt.Errorf("unsupported notification type %q", cfg.Type)
	}
}

func sendTelegram(cfg Config, text string) error {
	if cfg.Token == "" || cfg.ChatID == "" {
		return fmt.Errorf("telegram needs token and chat_id")
	}
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Token)
	form := url.Values{"chat_id": {cfg.ChatID}, "text": {text}}
	resp, err := client.PostForm(api, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned %s", resp.Status)
	}
	return nil
}

func sendDiscord(cfg Config, text string) error {
	return postJSON(cfg.URL, map[string]string{"content": text})
}

func sendWebhook(cfg Config, text string) error {
	return postJSON(cfg.URL, map[string]string{"text": text})
}

func postJSON(u string, payload any) error {
	if u == "" {
		return fmt.Errorf("missing URL")
	}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
