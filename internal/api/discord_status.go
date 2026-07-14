package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Discord status board: keeps a single, auto-updating embed in a Discord channel
// showing your public servers' up/down state and player counts — a live status
// board for a community, with NO bot to host. It only needs an incoming webhook
// URL; the panel edits the same message in place (never spams the channel).
//
// It reuses the exact set the public /status page shows (servers with
// status_public=1), so a server you "share" appears both on the web status page
// and in Discord. Off until an admin pastes a webhook URL and enables it.

const discordStatusInterval = 3 * time.Minute

const (
	discordGreen = 3066993  // all shared servers online
	discordAmber = 15844367 // some online, some not
	discordGrey  = 9807270  // none online / nothing shared
)

func (s *Server) discordStatusEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "discord_status_enabled") == "1" && s.discordWebhookURL(ctx) != ""
}

// discordWebhookURL returns the decrypted webhook URL ("" when unset/undecodable).
func (s *Server) discordWebhookURL(ctx context.Context) string {
	enc := s.getSetting(ctx, "discord_status_webhook")
	if enc == "" {
		return ""
	}
	url, err := s.cipher.Decrypt(enc)
	if err != nil {
		return ""
	}
	return url
}

// buildDiscordStatusPayload renders the webhook body (one embed) from the current
// public status. Reuses buildPublicStatus so the Discord board and /status agree.
func (s *Server) buildDiscordStatusPayload(ctx context.Context) []byte {
	st := s.buildPublicStatus(ctx)

	type field struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline"`
	}
	fields := []field{}
	online := 0
	for _, sv := range st.Servers {
		if len(fields) >= 25 { // Discord's hard cap on embed fields
			break
		}
		dot := "🔴"
		val := sv.Game
		switch sv.Status {
		case "online":
			dot = "🟢"
			online++
			if sv.Players != nil {
				val = fmt.Sprintf("%s · %d online", sv.Game, *sv.Players)
			}
		case "starting":
			dot = "🟡"
			val = sv.Game + " · starting…"
		default:
			val = sv.Game + " · offline"
		}
		if strings.TrimSpace(val) == "" {
			val = sv.Status
		}
		fields = append(fields, field{Name: dot + " " + sv.Name, Value: val, Inline: true})
	}

	color := discordGrey
	if len(st.Servers) > 0 {
		switch {
		case online == len(st.Servers):
			color = discordGreen
		case online > 0:
			color = discordAmber
		default:
			color = discordGrey
		}
	}

	embed := map[string]any{
		"title":     st.Title,
		"color":     color,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"footer":    map[string]string{"text": "Yggdrasil Panel · auto-updated"},
	}
	if len(fields) > 0 {
		embed["fields"] = fields
		embed["description"] = fmt.Sprintf("**%d of %d online**", online, len(st.Servers))
	} else {
		embed["description"] = "No servers are being shared right now."
	}
	body, _ := json.Marshal(map[string]any{"embeds": []any{embed}})
	return body
}

// postOrUpdateDiscordStatus keeps a single message current: it edits the stored
// message if there is one, and posts a fresh one (recording its id) otherwise — or
// if the stored message was deleted. Best-effort; returns an error for the manual
// "Post now" path to surface.
func (s *Server) postOrUpdateDiscordStatus(ctx context.Context) error {
	url := s.discordWebhookURL(ctx)
	if url == "" {
		return fmt.Errorf("no webhook configured")
	}
	body := s.buildDiscordStatusPayload(ctx)

	if msgID := s.getSetting(ctx, "discord_status_message_id"); msgID != "" {
		code, err := s.discordEdit(ctx, url, msgID, body)
		if err == nil && code >= 200 && code < 300 {
			return nil
		}
		// 404 = the message was deleted in Discord; fall through and post a new one.
		if code != http.StatusNotFound {
			if err != nil {
				return err
			}
			return fmt.Errorf("discord edit failed: HTTP %d", code)
		}
		s.setSetting(ctx, "discord_status_message_id", "")
	}

	id, err := s.discordPost(ctx, url, body)
	if err != nil {
		return err
	}
	if id != "" {
		s.setSetting(ctx, "discord_status_message_id", id)
	}
	return nil
}

// discordPost creates the message (?wait=true so Discord returns it) and returns
// its id so we can edit it next time.
func (s *Server) discordPost(ctx context.Context, webhook string, body []byte) (string, error) {
	sep := "?"
	if strings.Contains(webhook, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", webhook+sep+"wait=true", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("discord webhook post failed: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	return out.ID, nil
}

// discordEdit PATCHes an existing webhook message; returns the HTTP status.
func (s *Server) discordEdit(ctx context.Context, webhook, messageID string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "PATCH", webhook+"/messages/"+messageID, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// startDiscordStatusLoop periodically refreshes the board while it's enabled.
func (s *Server) startDiscordStatusLoop() {
	go func() {
		defer recoverLog("discordStatusLoop")
		t := time.NewTicker(discordStatusInterval)
		defer t.Stop()
		for range t.C {
			ctx := context.Background()
			if s.discordStatusEnabled(ctx) {
				if err := s.postOrUpdateDiscordStatus(ctx); err != nil {
					log.Printf("discord status update failed: %v", err)
				}
			}
		}
	}()
}

// --- Settings (admin) ---

func (s *Server) handleGetDiscordStatus(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"enabled":    s.getSetting(r.Context(), "discord_status_enabled") == "1",
		"configured": s.getSetting(r.Context(), "discord_status_webhook") != "",
	})
}

func (s *Server) handleSetDiscordStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool   `json:"enabled"`
		Webhook *string `json:"webhook"` // nil = leave unchanged; "" = clear
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if req.Webhook != nil {
		wh := strings.TrimSpace(*req.Webhook)
		if wh == "" {
			s.setSetting(ctx, "discord_status_webhook", "")
			s.setSetting(ctx, "discord_status_message_id", "") // new webhook ⇒ new message next time
		} else if !strings.HasPrefix(wh, "https://") {
			jsonError(w, "webhook URL must start with https://", http.StatusBadRequest)
			return
		} else if enc, err := s.cipher.Encrypt(wh); err == nil {
			s.setSetting(ctx, "discord_status_webhook", enc)
			s.setSetting(ctx, "discord_status_message_id", "")
		}
	}
	if req.Enabled != nil {
		s.setSetting(ctx, "discord_status_enabled", boolStr(*req.Enabled))
	}
	s.auditLog(r, "settings.discord_status", "discord_status", map[string]any{"enabled": req.Enabled != nil && *req.Enabled})
	// Push an immediate update so the board reflects the change right away.
	if s.discordStatusEnabled(ctx) {
		go s.postOrUpdateDiscordStatus(context.Background())
	}
	s.handleGetDiscordStatus(w, r)
}

// handlePostDiscordStatus forces an immediate refresh ("Post now" / test).
func (s *Server) handlePostDiscordStatus(w http.ResponseWriter, r *http.Request) {
	if s.discordWebhookURL(r.Context()) == "" {
		jsonError(w, "paste a webhook URL first", http.StatusBadRequest)
		return
	}
	if err := s.postOrUpdateDiscordStatus(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}
