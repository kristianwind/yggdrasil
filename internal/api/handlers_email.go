package api

import (
	"context"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/notify"
)

// The panel's global outbound-SMTP configuration lives in app_settings under the
// smtp_* keys (password encrypted at rest, everything else plain). It's what
// transactional mail — password-reset links and the "send test" button — uses,
// separate from the per-channel notification email in the notifications table.

// smtpConfig builds a notify.Config from the stored SMTP settings, decrypting
// the password. ok is false when the essentials (host + from) aren't set, so a
// caller can tell "no mailer configured" apart from a send that failed.
func (s *Server) smtpConfig(ctx context.Context) (notify.Config, bool) {
	host := s.getSetting(ctx, "smtp_host")
	from := s.getSetting(ctx, "smtp_from")
	if host == "" || from == "" {
		return notify.Config{}, false
	}
	port, _ := strconv.Atoi(s.getSetting(ctx, "smtp_port"))
	pw := ""
	if enc := s.getSetting(ctx, "smtp_password"); enc != "" {
		if dec, err := s.cipher.Decrypt(enc); err == nil {
			pw = dec
		}
	}
	return notify.Config{
		Type:     "email",
		Host:     host,
		Port:     port,
		Username: s.getSetting(ctx, "smtp_username"),
		Password: pw,
		From:     from,
	}, true
}

// handleGetEmailSettings returns the SMTP config for the Settings UI. It never
// returns the password itself — only whether one is stored.
func (s *Server) handleGetEmailSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := s.getSetting(ctx, "smtp_host")
	from := s.getSetting(ctx, "smtp_from")
	jsonOK(w, map[string]any{
		"host":         host,
		"port":         s.getSetting(ctx, "smtp_port"),
		"username":     s.getSetting(ctx, "smtp_username"),
		"from":         from,
		"has_password": s.getSetting(ctx, "smtp_password") != "",
		"configured":   host != "" && from != "",
	})
}

// handleSetEmailSettings updates the SMTP config. The password follows the
// pointer idiom used elsewhere (nil = keep the stored one so the UI needn't
// re-send it; "" = clear it); it's encrypted at rest like other secrets.
func (s *Server) handleSetEmailSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string  `json:"host"`
		Port     string  `json:"port"`
		Username string  `json:"username"`
		From     string  `json:"from"`
		Password *string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	from := strings.TrimSpace(req.From)
	if from != "" {
		if _, err := mail.ParseAddress(from); err != nil {
			jsonError(w, "From must be a valid email address", http.StatusBadRequest)
			return
		}
	}
	ctx := r.Context()
	s.setSetting(ctx, "smtp_host", strings.TrimSpace(req.Host))
	s.setSetting(ctx, "smtp_port", strings.TrimSpace(req.Port))
	s.setSetting(ctx, "smtp_username", strings.TrimSpace(req.Username))
	s.setSetting(ctx, "smtp_from", from)
	if req.Password != nil {
		if strings.TrimSpace(*req.Password) == "" {
			s.setSetting(ctx, "smtp_password", "")
		} else if enc, err := s.cipher.Encrypt(*req.Password); err == nil {
			s.setSetting(ctx, "smtp_password", enc)
		}
	}
	s.auditLog(r, "settings.email", "smtp", map[string]string{"host": strings.TrimSpace(req.Host), "from": from})
	jsonOK(w, map[string]string{"status": "saved"})
}

// handleTestEmail sends a one-off message to prove the SMTP settings work,
// backing the "Send test" button. Defaults to the From address when no
// recipient is given.
func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	cfg, ok := s.smtpConfig(r.Context())
	if !ok {
		jsonError(w, "set the SMTP host and From address first", http.StatusBadRequest)
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = cfg.From
	}
	if _, err := mail.ParseAddress(to); err != nil {
		jsonError(w, "invalid recipient address", http.StatusBadRequest)
		return
	}
	cfg.To = to
	if err := notify.SendEmail(cfg, "Yggdrasil test email",
		"This is a test email from your Yggdrasil panel.\r\n\r\nIf you can read this, outbound SMTP is working. 🌳"); err != nil {
		jsonError(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "settings.email_test", "smtp", map[string]string{"to": to})
	jsonOK(w, map[string]string{"status": "sent"})
}
