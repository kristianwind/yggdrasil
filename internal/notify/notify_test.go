package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendDiscord(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	if err := Send(Config{Type: "discord", URL: srv.URL}, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got["content"] != "hello" {
		t.Errorf("discord payload content = %q", got["content"])
	}
}

func TestSendWebhook(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	if err := Send(Config{Type: "webhook", URL: srv.URL}, "ping"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got["text"] != "ping" {
		t.Errorf("webhook payload text = %q", got["text"])
	}
}

func TestSendWebhookErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := Send(Config{Type: "webhook", URL: srv.URL}, "x"); err == nil {
		t.Error("expected error on 500")
	}
}

func TestUnsupportedType(t *testing.T) {
	if err := Send(Config{Type: "carrier-pigeon"}, "x"); err == nil {
		t.Error("expected error for unsupported type")
	}
}
