package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLLM serves a canned OpenAI/Anthropic response body so Complete's parsing and
// empty-handling can be exercised without a real model.
func fakeLLM(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A verbose model that spends its whole max_tokens budget without emitting text
// comes back with empty content and finish_reason "length". The panel showed that
// as "(no summary returned)" because Complete returned ("", nil) — a blank passed
// off as a valid answer. It must be an error instead, and one that names the fix.
func TestCompleteOpenAIEmptyLengthIsError(t *testing.T) {
	base := fakeLLM(t, `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`)
	out, err := Complete(context.Background(),
		Config{Provider: "custom", Model: "gemma-4-12b-qat", BaseURL: base, APIKey: "k"},
		[]Message{{Role: "user", Content: "digest please"}}, 700)
	if out != "" {
		t.Fatalf("expected no text, got %q", out)
	}
	if !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("expected ErrEmptyCompletion, got %v", err)
	}
	if !strings.Contains(err.Error(), "token limit") {
		t.Errorf("error should name the token limit as the fix, got %q", err.Error())
	}
}

// An empty completion that did NOT hit the cap is still an error, just without the
// token-limit hint.
func TestCompleteOpenAIEmptyStopIsError(t *testing.T) {
	base := fakeLLM(t, `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`)
	_, err := Complete(context.Background(),
		Config{Provider: "custom", Model: "m", BaseURL: base, APIKey: "k"},
		[]Message{{Role: "user", Content: "x"}}, 700)
	if !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("expected ErrEmptyCompletion, got %v", err)
	}
}

// A normal reply still comes back as text, trimmed, with no error.
func TestCompleteOpenAINonEmpty(t *testing.T) {
	base := fakeLLM(t, `{"choices":[{"message":{"content":"  all systems clear.\n"},"finish_reason":"stop"}]}`)
	out, err := Complete(context.Background(),
		Config{Provider: "custom", Model: "m", BaseURL: base, APIKey: "k"},
		[]Message{{Role: "user", Content: "x"}}, 700)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "all systems clear." {
		t.Errorf("got %q, want trimmed text", out)
	}
}

// The Anthropic path must behave the same: empty text → ErrEmptyCompletion, and
// stop_reason max_tokens names the token limit.
func TestCompleteAnthropicEmptyIsError(t *testing.T) {
	base := fakeLLM(t, `{"content":[{"type":"text","text":""}],"stop_reason":"max_tokens"}`)
	_, err := Complete(context.Background(),
		Config{Provider: "anthropic", Model: "claude", APIKey: "k", BaseURL: base},
		[]Message{{Role: "user", Content: "x"}}, 700)
	if !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("expected ErrEmptyCompletion, got %v", err)
	}
	if !strings.Contains(err.Error(), "token limit") {
		t.Errorf("stop_reason max_tokens should name the token limit, got %q", err.Error())
	}
}
