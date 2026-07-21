package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/notify"
)

// Notification channels are global-admin config; secrets are encrypted at rest.

type notifyView struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Enabled    bool   `json:"enabled"`
	ServerID   string `json:"server_id"`   // "" = global (every notification)
	ServerName string `json:"server_name"` // resolved for display
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT n.id, n.type, n.enabled, COALESCE(n.server_id,''), COALESCE(srv.name,'')
		 FROM notifications n LEFT JOIN servers srv ON srv.id = n.server_id
		 ORDER BY n.created_at`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []notifyView{}
	for rows.Next() {
		var v notifyView
		var enabled int
		if err := rows.Scan(&v.ID, &v.Type, &enabled, &v.ServerID, &v.ServerName); err != nil {
			continue
		}
		v.Enabled = enabled == 1
		list = append(list, v)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		notify.Config
		ServerID string `json:"server_id"` // "" = global
	}
	if err := decodeJSON(r, &req); err != nil || req.Type == "" {
		jsonError(w, "type required", http.StatusBadRequest)
		return
	}
	cfg := req.Config
	enc, err := s.encryptNotify(cfg)
	if err != nil {
		jsonError(w, "encrypt error", http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO notifications (id, type, config_enc, server_id, enabled) VALUES (?,?,?,?,1)",
		id, cfg.Type, enc, nullableStr(strings.TrimSpace(req.ServerID))); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "notification.create", "notification:"+id, map[string]string{"type": cfg.Type, "server_id": req.ServerID})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM notifications WHERE id=?", id)
	s.auditLog(r, "notification.delete", "notification:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var enc string
	if err := s.db.QueryRowContext(r.Context(), "SELECT config_enc FROM notifications WHERE id=?", id).Scan(&enc); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	cfg, err := s.decryptNotify(enc)
	if err != nil {
		jsonError(w, "decrypt error", http.StatusInternalServerError)
		return
	}
	if err := notify.Send(cfg, "🌳 Yggdrasil test notification — channels are working."); err != nil {
		jsonError(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]string{"status": "sent"})
}

// notifyAll sends text to every enabled channel, in the background. It is the
// single entry point event hooks call (e.g. backup done/failed, server up/down).
// notifyAll sends to every GLOBAL channel (one with no server scope). Use it for
// host-wide notifications (disk, AI digest, cross-server schedule summaries).
func (s *Server) notifyAll(text string) { s.notifyChannels("", text) }

// notifyServer sends to the global channels PLUS any channel scoped to serverID —
// so a per-server channel receives only that server's events, while global channels
// still see everything. Use it for anything about one specific server.
func (s *Server) notifyServer(serverID, text string) { s.notifyChannels(serverID, text) }

func (s *Server) notifyChannels(serverID, text string) {
	go func() {
		q := "SELECT config_enc FROM notifications WHERE enabled=1 AND COALESCE(server_id,'')=''"
		var args []any
		if serverID != "" {
			q = "SELECT config_enc FROM notifications WHERE enabled=1 AND (COALESCE(server_id,'')='' OR server_id=?)"
			args = append(args, serverID)
		}
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		var encs []string
		for rows.Next() {
			var enc string
			if rows.Scan(&enc) == nil {
				encs = append(encs, enc)
			}
		}
		for _, enc := range encs {
			cfg, err := s.decryptNotify(enc)
			if err != nil {
				continue
			}
			if err := notify.Send(cfg, text); err != nil {
				log.Printf("notify: %s send failed: %v", cfg.Type, err)
			}
		}
	}()
}

func (s *Server) encryptNotify(cfg notify.Config) (string, error) {
	b, _ := json.Marshal(cfg)
	return s.cipher.Encrypt(string(b))
}

func (s *Server) decryptNotify(enc string) (notify.Config, error) {
	var cfg notify.Config
	plain, err := s.cipher.Decrypt(enc)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(plain), &cfg)
	return cfg, err
}
