// Package notify delivers event notifications to Telegram, Discord, or a generic
// webhook. Channel credentials are encrypted at rest by the caller.
package notify

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"time"
)

// Config is a decrypted notification channel.
type Config struct {
	Type   string `json:"type"`              // telegram | discord | webhook | email
	Token  string `json:"token,omitempty"`   // telegram bot token
	ChatID string `json:"chat_id,omitempty"` // telegram chat id
	URL    string `json:"url,omitempty"`     // discord/webhook URL
	// Email (SMTP)
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
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
	case "email":
		return sendEmail(cfg, text)
	default:
		return fmt.Errorf("unsupported notification type %q", cfg.Type)
	}
}

func sendEmail(cfg Config, text string) error {
	if cfg.Host == "" || cfg.From == "" || cfg.To == "" {
		return fmt.Errorf("email needs host, from and to")
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := cfg.Host + ":" + strconv.Itoa(port)
	msg := []byte("From: " + cfg.From + "\r\n" +
		"To: " + cfg.To + "\r\n" +
		"Subject: Yggdrasil notification\r\n" +
		"\r\n" + text + "\r\n")
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	return sendMailTimeout(addr, cfg.Host, auth, cfg.From, []string{cfg.To}, msg)
}

// smtpDeadline bounds a whole SMTP conversation — dial, handshake, and every
// command after it.
//
// The other channels go through an http.Client with a 10s timeout. smtp.SendMail
// has no timeout at all: it dials without a deadline and then blocks on reads, so
// a host that accepts the connection and stops answering hangs forever. That
// leaks a goroutine per background notification, and — worse — hangs the request
// outright when it's the synchronous "Test" button, which has no server-side
// timeout to save it.
// A var rather than a const so the test can shorten it; nothing else reassigns it.
var smtpDeadline = 20 * time.Second

// sendMailTimeout is smtp.SendMail with a deadline on the connection. The steps
// mirror the standard library's, including opportunistic STARTTLS: the deadline
// is the only difference.
func sendMailTimeout(addr, host string, a smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := net.DialTimeout("tcp", addr, smtpDeadline)
	if err != nil {
		return err
	}
	// One deadline for the whole exchange, so no individual step can stall.
	if err := conn.SetDeadline(time.Now().Add(smtpDeadline)); err != nil {
		conn.Close()
		return err
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if a != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(a); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
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
