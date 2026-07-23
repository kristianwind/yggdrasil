package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CompleteStream is Complete with incremental delivery: onDelta is called with
// each chunk of assistant text as it arrives, and the full reply is returned at
// the end (same empty-reply semantics as Complete). Only the OpenAI-compatible
// wire format streams — it's what every self-hosted gateway speaks, and the
// interactive chat this exists for runs against those. Anthropic falls back to
// one buffered call delivered as a single delta, so callers need no special
// case.
func CompleteStream(ctx context.Context, cfg Config, messages []Message, maxTokens int, onDelta func(string)) (string, error) {
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
		out, err := completeAnthropic(ctx, cfg, base, messages, maxTokens)
		if err == nil && onDelta != nil {
			onDelta(out)
		}
		return out, err
	}
	return streamOpenAI(ctx, cfg, base, messages, maxTokens, onDelta)
}

// streamClient has no overall timeout — a chat reply legitimately takes minutes
// on a slow local model, and the tokens keep flowing the whole time. The caller
// bounds the exchange through ctx.
var streamClient = &http.Client{Timeout: 0}

func streamOpenAI(ctx context.Context, cfg Config, base string, messages []Message, maxTokens int, onDelta func(string)) (string, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	ms := make([]msg, 0, len(messages))
	for _, m := range messages {
		ms = append(ms, msg{Role: m.Role, Content: m.Content})
	}
	reqBody := map[string]any{
		"model":      cfg.Model,
		"messages":   ms,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	// Same thinking-model guard as Complete — see the comment there.
	if !strings.Contains(strings.ToLower(base), "openai.com") {
		reqBody["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", apiError(resp.StatusCode, raw)
	}

	// SSE: lines of "data: {chunk json}", finished by "data: [DONE]". A gateway
	// that ignored stream:true sends one plain JSON object instead — detected and
	// handled below so a non-streaming backend still works.
	var sb strings.Builder
	finish := ""
	sawSSE := false
	var plain bytes.Buffer
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	deadline := time.Now().Add(10 * time.Minute) // hard backstop against a wedged stream
	for sc.Scan() {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("stream ran past the 10-minute backstop")
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			if !sawSSE {
				plain.WriteString(line) // maybe a non-streaming JSON body
			}
			continue
		}
		sawSSE = true
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		if c := chunk.Choices[0]; c.Delta.Content != "" {
			sb.WriteString(c.Delta.Content)
			if onDelta != nil {
				onDelta(c.Delta.Content)
			}
		} else if c.FinishReason != nil {
			finish = *c.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("stream read: %w", err)
	}

	if !sawSSE && plain.Len() > 0 {
		// Non-streaming backend: parse the buffered completion instead.
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(plain.Bytes(), &out) == nil && len(out.Choices) > 0 {
			text := strings.TrimSpace(out.Choices[0].Message.Content)
			if text != "" {
				if onDelta != nil {
					onDelta(text)
				}
				return text, nil
			}
			finish = out.Choices[0].FinishReason
		}
	}

	text := strings.TrimSpace(sb.String())
	if text == "" {
		if finish == "length" {
			return "", fmt.Errorf("%w — it hit the token limit before answering (raise max_tokens or use a less verbose model)", ErrEmptyCompletion)
		}
		return "", ErrEmptyCompletion
	}
	return text, nil
}
