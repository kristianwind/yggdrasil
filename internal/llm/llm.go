// Package llm is a small provider-agnostic chat-completion client for
// Yggdrasil's advisory AI features (e.g. the admin-log "what happened while I
// was away" digest). It speaks two wire formats: Anthropic's Messages API and
// the OpenAI /chat/completions shape that OpenRouter, DeepSeek, Mistral, Ollama
// and most self-hosted gateways also implement. The user brings their own key
// and endpoint — nothing is sent anywhere the operator hasn't configured.
package llm

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

// Config describes which model to call and how to reach it.
type Config struct {
	Provider string // anthropic | openai | openrouter | deepseek | mistral | ollama | custom | auto
	Model    string
	BaseURL  string // optional; overrides the provider default (required for custom)
	APIKey   string
}

// Message is one turn in the conversation.
type Message struct {
	Role    string // system | user | assistant
	Content string
}

// defaultBaseURL returns the API base for a known provider (no trailing slash).
func defaultBaseURL(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "https://api.anthropic.com"
	case "openai", "auto", "":
		return "https://api.openai.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "ollama":
		return "http://127.0.0.1:11434/v1"
	default:
		return ""
	}
}

// resolveBaseURL picks the effective base URL (explicit override wins).
func (c Config) resolveBaseURL() (string, error) {
	if b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); b != "" {
		return b, nil
	}
	if b := defaultBaseURL(c.Provider); b != "" {
		return b, nil
	}
	return "", fmt.Errorf("provider %q needs an explicit base URL", c.Provider)
}

func (c Config) isAnthropic() bool {
	return strings.EqualFold(strings.TrimSpace(c.Provider), "anthropic")
}

// Complete sends the messages to the configured model and returns the assistant's
// reply text. maxTokens caps the response length. The caller supplies the ctx
// (and thus the timeout).
func Complete(ctx context.Context, cfg Config, messages []Message, maxTokens int) (string, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return "", fmt.Errorf("no model configured")
	}
	if strings.TrimSpace(cfg.APIKey) == "" && !strings.EqualFold(cfg.Provider, "ollama") {
		return "", fmt.Errorf("no API key configured")
	}
	base, err := cfg.resolveBaseURL()
	if err != nil {
		return "", err
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	if cfg.isAnthropic() {
		return completeAnthropic(ctx, cfg, base, messages, maxTokens)
	}
	return completeOpenAI(ctx, cfg, base, messages, maxTokens)
}

var httpClient = &http.Client{Timeout: 90 * time.Second}

// ---- OpenAI-compatible (/chat/completions) ----

func completeOpenAI(ctx context.Context, cfg Config, base string, messages []Message, maxTokens int) (string, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	ms := make([]msg, 0, len(messages))
	for _, m := range messages {
		ms = append(ms, msg{Role: m.Role, Content: m.Content})
	}
	body, _ := json.Marshal(map[string]any{
		"model":      cfg.Model,
		"messages":   ms,
		"max_tokens": maxTokens,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", apiError(resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model returned no choices")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// ---- Anthropic (/v1/messages) ----

func completeAnthropic(ctx context.Context, cfg Config, base string, messages []Message, maxTokens int) (string, error) {
	// Anthropic takes the system prompt out-of-band and only user/assistant turns
	// in messages.
	var system string
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var ms []msg
	for _, m := range messages {
		if m.Role == "system" {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}
		ms = append(ms, msg{Role: m.Role, Content: m.Content})
	}
	payload := map[string]any{
		"model":      cfg.Model,
		"max_tokens": maxTokens,
		"messages":   ms,
	}
	if system != "" {
		payload["system"] = system
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", apiError(resp.StatusCode, raw)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	s := strings.TrimSpace(sb.String())
	if s == "" {
		return "", fmt.Errorf("model returned no text")
	}
	return s, nil
}

// apiError extracts a provider error message from a non-2xx response, falling
// back to the raw body. Never includes the request (which holds the API key).
func apiError(status int, raw []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := e.Error.Message
	if msg == "" {
		msg = e.Message
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Errorf("LLM API error (HTTP %d): %s", status, msg)
}
