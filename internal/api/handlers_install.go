package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// handleInstallServer kicks off (or re-runs) a server's install in the
// background. Progress is streamed via the install/log WebSocket.
func (s *Server) handleInstallServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var exists int
	if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM servers WHERE id=?", id).Scan(&exists); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if s.install.isActive(id) {
		jsonError(w, "install already running", http.StatusConflict)
		return
	}
	s.auditLog(r, "server.install", "server:"+id, nil)
	go s.runInstall(id) //nolint:errcheck // status persisted in DB, streamed to hub
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]string{"status": "installing"})
}

// handleInstallLog streams install/build output (history + live) over WebSocket.
func (s *Server) handleInstallLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, history := s.install.subscribe(id)
	defer s.install.unsubscribe(id, ch)

	for _, line := range history {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			return
		}
	}

	// Detect client disconnect without racing the unsubscribe/close.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
