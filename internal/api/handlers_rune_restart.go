package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Restarting every server that uses a rune.
//
// A rune change does not reach a running server on its own. Restart recreates the
// container — that is how a new PUID default, a changed startup command or a new
// image gets picked up — so after editing a rune the work is "restart each server
// that uses it", and nothing in the panel would tell you which ones those were.
// On the game box that is eight Minecraft servers and six DayZ ones.
//
// Two decisions worth keeping:
//
//   - SEQUENTIAL. Restarting eight game servers at once is a bad afternoon, and
//     the temptation to parallelise it is exactly why it should not be. Players
//     come back one server at a time.
//   - It does not stop on the first failure. One server whose image has gone from
//     the registry must not leave the other seven on the old configuration; the
//     failures are collected and reported at the end.
//
// Only servers that are currently running are touched. A stopped server picks the
// rune up whenever someone starts it, and starting something the operator had
// deliberately stopped would be a surprise nobody asked for.
type runeRestartState struct {
	mu     sync.Mutex
	active map[string]bool // gameskill id -> a sweep is running
}

func newRuneRestartState() *runeRestartState {
	return &runeRestartState{active: map[string]bool{}}
}

func (r *runeRestartState) begin(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[id] {
		return false
	}
	r.active[id] = true
	return true
}

func (r *runeRestartState) end(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, id)
}

type runeServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// runeServers lists the running servers a rune restart would touch.
func (s *Server) runeServers(ctx context.Context, gameskillID string) []runeServer {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM servers
		  WHERE gameskill_id = ? AND installed = 1 AND status IN ('running','starting')
		  ORDER BY name`, gameskillID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []runeServer{}
	for rows.Next() {
		var x runeServer
		if rows.Scan(&x.ID, &x.Name) == nil {
			out = append(out, x)
		}
	}
	return out
}

// handleRuneServers reports which servers a restart would affect, so the UI can
// name them before anyone presses the button. Showing the list is the point: "restart
// 8 servers" is a very different decision from "restart the one you were looking at".
func (s *Server) handleRuneServers(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{"servers": s.runeServers(r.Context(), chi.URLParam(r, "id"))})
}

// handleRestartRuneServers restarts them, one at a time, in the background.
func (s *Server) handleRestartRuneServers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	list := s.runeServers(r.Context(), id)
	if len(list) == 0 {
		jsonError(w, "no running servers use this rune", http.StatusConflict)
		return
	}
	if !s.runeRestarts.begin(id) {
		jsonError(w, "a restart of this rune's servers is already running", http.StatusConflict)
		return
	}

	var name string
	s.db.QueryRowContext(r.Context(), "SELECT name FROM gameskills WHERE id=?", id).Scan(&name)
	if name == "" {
		name = id
	}
	s.auditLog(r, "rune.restart_servers", "gameskill:"+id, map[string]any{"servers": len(list)})

	go func() {
		defer recoverLog("restartRuneServers")
		defer s.runeRestarts.end(id)
		ctx := context.Background()

		var failed []string
		for _, srv := range list {
			// Someone else may be installing or updating this one; recreating the
			// container underneath that would corrupt what it is halfway through.
			if s.install.isActive(srv.ID) {
				failed = append(failed, srv.Name+" (busy)")
				continue
			}
			if err := s.recreateAndStart(ctx, srv.ID); err != nil {
				log.Printf("rune restart: %s: %v", srv.Name, err)
				failed = append(failed, srv.Name)
				continue
			}
			// Breathe between servers. Recreate pulls the image, and eight pulls
			// launched back to back on one host is its own kind of outage.
			time.Sleep(3 * time.Second)
		}

		msg := fmt.Sprintf("🔄 Restarted %d server(s) using **%s**", len(list)-len(failed), name)
		if len(failed) > 0 {
			msg += fmt.Sprintf("\n⚠️ Could not restart: %v — they are still on the old configuration", failed)
		}
		s.notifyAll(msg)
	}()

	jsonOK(w, map[string]any{"started": len(list), "servers": list})
}
