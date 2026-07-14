package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/crypto"
)

// A running-and-shared server produces a green embed; the first refresh POSTs (and
// records the returned message id) while later refreshes PATCH that same message.
func TestDiscordStatusPostThenEdit(t *testing.T) {
	s := testServer(t)
	cipher, err := crypto.New("test-secret-key-abcdef-0123456789")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	s.cipher = cipher

	// One shared, running server with a recent player sample.
	s.db.Exec("INSERT INTO gameskills (id, name, yaml_blob) VALUES ('mc','Minecraft (Java)','id: mc')")
	s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, installed, data_dir, status_public)
		VALUES ('s1','Asgard','mc','running',1,'/tmp/s1',1)`)
	s.db.Exec("INSERT INTO metrics (server_id, cpu, mem_mb, players) VALUES ('s1',1,1,4)")

	var posts, patches int
	var lastBody []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		lastBody = buf
		switch r.Method {
		case http.MethodPost:
			posts++
			if r.URL.Query().Get("wait") != "true" {
				t.Errorf("post should use ?wait=true, got %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"id":"msg-42"}`))
		case http.MethodPatch:
			patches++
			if !strings.HasSuffix(r.URL.Path, "/messages/msg-42") {
				t.Errorf("patch path = %q, want .../messages/msg-42", r.URL.Path)
			}
		}
	}))
	defer mock.Close()

	ctx := context.Background()
	enc, _ := cipher.Encrypt(mock.URL)
	s.setSetting(ctx, "discord_status_webhook", enc)

	// First refresh: POST + store id.
	if err := s.postOrUpdateDiscordStatus(ctx); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if posts != 1 || patches != 0 {
		t.Fatalf("after first: posts=%d patches=%d, want 1/0", posts, patches)
	}
	if got := s.getSetting(ctx, "discord_status_message_id"); got != "msg-42" {
		t.Fatalf("stored message id = %q, want msg-42", got)
	}

	// The payload is a green embed with the player count folded in.
	var payload struct {
		Embeds []struct {
			Color       int                            `json:"color"`
			Description string                         `json:"description"`
			Fields      []struct{ Name, Value string } `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(lastBody, &payload); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, lastBody)
	}
	if len(payload.Embeds) != 1 || payload.Embeds[0].Color != discordGreen {
		t.Fatalf("embed color = %d, want green %d", payload.Embeds[0].Color, discordGreen)
	}
	if len(payload.Embeds[0].Fields) != 1 || !strings.Contains(payload.Embeds[0].Fields[0].Value, "4 online") {
		t.Fatalf("field = %+v, want '…4 online'", payload.Embeds[0].Fields)
	}

	// Second refresh: PATCH the same message, no new POST.
	if err := s.postOrUpdateDiscordStatus(ctx); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if posts != 1 || patches != 1 {
		t.Fatalf("after second: posts=%d patches=%d, want 1/1", posts, patches)
	}
}

// A deleted message (404 on edit) is re-posted rather than left stale.
func TestDiscordStatusRepostsOn404(t *testing.T) {
	s := testServer(t)
	cipher, _ := crypto.New("test-secret-key-abcdef-0123456789")
	s.cipher = cipher

	var posts int
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNotFound) // message was deleted in Discord
			return
		}
		posts++
		w.Write([]byte(`{"id":"new-id"}`))
	}))
	defer mock.Close()

	ctx := context.Background()
	enc, _ := cipher.Encrypt(mock.URL)
	s.setSetting(ctx, "discord_status_webhook", enc)
	s.setSetting(ctx, "discord_status_message_id", "stale-id")

	if err := s.postOrUpdateDiscordStatus(ctx); err != nil {
		t.Fatalf("update: %v", err)
	}
	if posts != 1 {
		t.Fatalf("posts=%d, want 1 (repost after 404)", posts)
	}
	if got := s.getSetting(ctx, "discord_status_message_id"); got != "new-id" {
		t.Fatalf("message id = %q, want new-id", got)
	}
}
