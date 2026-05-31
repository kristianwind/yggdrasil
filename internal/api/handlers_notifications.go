package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/notify"
)

// Notification channels are global-admin config; secrets are encrypted at rest.

type notifyView struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, type, enabled FROM notifications ORDER BY created_at")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []notifyView{}
	for rows.Next() {
		var v notifyView
		var enabled int
		if err := rows.Scan(&v.ID, &v.Type, &enabled); err != nil {
			continue
		}
		v.Enabled = enabled == 1
		list = append(list, v)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	var cfg notify.Config
	if err := decodeJSON(r, &cfg); err != nil || cfg.Type == "" {
		jsonError(w, "type required", http.StatusBadRequest)
		return
	}
	enc, err := s.encryptNotify(cfg)
	if err != nil {
		jsonError(w, "encrypt error", http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO notifications (id, type, config_enc, enabled) VALUES (?,?,?,1)",
		id, cfg.Type, enc); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "notification.create", "notification:"+id, map[string]string{"type": cfg.Type})
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
func (s *Server) notifyAll(text string) {
	go func() {
		rows, err := s.db.Query("SELECT config_enc FROM notifications WHERE enabled=1")
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
