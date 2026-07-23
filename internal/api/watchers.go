package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Kvasir Watchers — the proactive log layer. A watcher is a user-defined rule:
// "if this pattern appears N times in the last W seconds of a server's log, do
// something." That something is a notification and, when the AI is on and the
// watcher's action is 'kvasir', a Kvasir reaction over the matched lines (explain
// what's happening + propose a fix). Works for any server — a game admin log, a
// WordPress error log, a database's slow-query log, an auth log — because it reads
// the container's own stdout/stderr, which is where those land.

const (
	watcherScanInterval = 30 * time.Second // how often every running server is scanned
	watcherCooldown     = 10 * time.Minute  // min gap between firings of one watcher
	watcherMaxWindow    = 3600              // clamp window_secs so a scan stays cheap
	watcherSampleLines  = 20                // matched lines handed to Kvasir / shown
)

type logWatcher struct {
	ID         string
	ServerID   string
	Name       string
	Pattern    string
	Threshold  int
	WindowSecs int
	Action     string
	Enabled    bool
	LastFired  string
}

// startWatcherLoop scans every running server's recent log against its enabled
// watchers on a fixed tick.
func (s *Server) startWatcherLoop() {
	go func() {
		defer recoverLog("watcherLoop")
		t := time.NewTicker(watcherScanInterval)
		defer t.Stop()
		for range t.C {
			s.scanWatchers()
		}
	}()
}

func (s *Server) scanWatchers() {
	defer recoverLog("scanWatchers")
	// Running servers with a container.
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status='running' AND container_id<>''")
	if err != nil {
		return
	}
	type sv struct{ id, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		for _, w := range s.watchersFor(x.id) {
			s.runWatcher(x.id, x.cid, w)
		}
	}
}

// watchersFor returns the enabled watchers that apply to a server (its own plus
// the global server_id='' ones).
func (s *Server) watchersFor(serverID string) []logWatcher {
	rows, err := s.db.Query(
		`SELECT id, server_id, name, pattern, threshold, window_secs, action, enabled, COALESCE(last_fired,'')
		 FROM log_watchers WHERE enabled=1 AND (server_id=? OR server_id='')`, serverID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []logWatcher
	for rows.Next() {
		var w logWatcher
		var en int
		if rows.Scan(&w.ID, &w.ServerID, &w.Name, &w.Pattern, &w.Threshold, &w.WindowSecs, &w.Action, &en, &w.LastFired) == nil {
			w.Enabled = en == 1
			out = append(out, w)
		}
	}
	return out
}

// runWatcher reads the last window_secs of the container log, counts pattern
// matches and fires when the threshold is met (respecting the cooldown).
func (s *Server) runWatcher(serverID, containerID string, w logWatcher) {
	if s.watcherCoolingDown(w) {
		return
	}
	re, err := regexp.Compile(w.Pattern)
	if err != nil {
		return // a bad regex simply never matches; the editor validates on save
	}
	win := w.WindowSecs
	if win <= 0 {
		win = 60
	}
	if win > watcherMaxWindow {
		win = watcherMaxWindow
	}
	lines := s.watcherLogLines(containerID, fmt.Sprintf("%ds", win))
	var matched []string
	for _, line := range lines {
		if re.MatchString(line) {
			matched = append(matched, stripANSI(strings.TrimSpace(line)))
		}
	}
	thr := w.Threshold
	if thr < 1 {
		thr = 1
	}
	if len(matched) < thr {
		return
	}
	s.fireWatcher(serverID, w, matched)
}

func (s *Server) watcherCoolingDown(w logWatcher) bool {
	if w.LastFired == "" {
		return false
	}
	t, err := time.Parse("2006-01-02 15:04:05", w.LastFired)
	if err != nil {
		return false
	}
	return time.Since(t.UTC()) < watcherCooldown
}

// watcherLogLines returns the demuxed lines of a container's log over the given
// Docker "since" window.
func (s *Server) watcherLogLines(containerID, since string) []string {
	rc, err := s.docker.LogsExport(context.Background(), containerID, docker.LogExportOptions{Since: since, Tail: "2000"})
	if err != nil {
		return nil
	}
	defer rc.Close()
	var buf bytes.Buffer
	_ = docker.DemuxCopy(&buf, rc)
	return strings.Split(buf.String(), "\n")
}

func (s *Server) fireWatcher(serverID string, w logWatcher, matched []string) {
	s.db.Exec("UPDATE log_watchers SET last_fired=datetime('now') WHERE id=?", w.ID)
	sample := matched
	if len(sample) > watcherSampleLines {
		sample = sample[len(sample)-watcherSampleLines:]
	}
	tail := strings.Join(sample, "\n")
	body := fmt.Sprintf("👁 Watcher **%s** tripped — %d matches in the last %ds", w.Name, len(matched), w.WindowSecs)
	if tail != "" {
		body += "\n```\n" + tail + "\n```"
	}
	go s.notifyServer(serverID, body)
	if w.Action == "kvasir" {
		go s.kvasirReact(serverID, "watcher", fmt.Sprintf("%s: %d matches in %ds", w.Name, len(matched), w.WindowSecs), tail)
	}
}

// ---- CRUD ----

type watcherDTO struct {
	ID         string `json:"id"`
	ServerID   string `json:"server_id"`
	Name       string `json:"name"`
	Pattern    string `json:"pattern"`
	Threshold  int    `json:"threshold"`
	WindowSecs int    `json:"window_secs"`
	Action     string `json:"action"`
	Enabled    bool   `json:"enabled"`
	LastFired  string `json:"last_fired,omitempty"`
	Source     string `json:"source,omitempty"` // '' = user-made, 'rune' = seeded from the rune
}

// handleListWatchers lists watchers (admin only — they read logs across servers).
func (s *Server) handleListWatchers(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("server_id")
	q := "SELECT id, server_id, name, pattern, threshold, window_secs, action, enabled, COALESCE(last_fired,''), COALESCE(source,'') FROM log_watchers"
	args := []any{}
	if sid != "" {
		q += " WHERE server_id=? OR server_id=''"
		args = append(args, sid)
	}
	q += " ORDER BY created_at DESC"
	rows, err := s.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []watcherDTO{}
	for rows.Next() {
		var d watcherDTO
		var en int
		if rows.Scan(&d.ID, &d.ServerID, &d.Name, &d.Pattern, &d.Threshold, &d.WindowSecs, &d.Action, &en, &d.LastFired, &d.Source) == nil {
			d.Enabled = en == 1
			list = append(list, d)
		}
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateWatcher(w http.ResponseWriter, r *http.Request) {
	var d watcherDTO
	if decodeJSON(r, &d) != nil || strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Pattern) == "" {
		jsonError(w, "name and pattern are required", http.StatusBadRequest)
		return
	}
	if _, err := regexp.Compile(d.Pattern); err != nil {
		jsonError(w, "invalid regular expression: "+err.Error(), http.StatusBadRequest)
		return
	}
	d.ID = uuid.New().String()
	if d.Threshold < 1 {
		d.Threshold = 1
	}
	if d.WindowSecs < 1 {
		d.WindowSecs = 60
	}
	if d.Action != "kvasir" {
		d.Action = "notify"
	}
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO log_watchers (id, server_id, name, pattern, threshold, window_secs, action, enabled)
		 VALUES (?,?,?,?,?,?,?,?)`,
		d.ID, strings.TrimSpace(d.ServerID), d.Name, d.Pattern, d.Threshold, d.WindowSecs, d.Action, boolToInt(d.Enabled))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "watcher.create", "watcher:"+d.ID, map[string]any{"name": d.Name, "server_id": d.ServerID})
	jsonOK(w, d)
}

func (s *Server) handleUpdateWatcher(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var d watcherDTO
	if decodeJSON(r, &d) != nil || strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Pattern) == "" {
		jsonError(w, "name and pattern are required", http.StatusBadRequest)
		return
	}
	if _, err := regexp.Compile(d.Pattern); err != nil {
		jsonError(w, "invalid regular expression: "+err.Error(), http.StatusBadRequest)
		return
	}
	if d.Threshold < 1 {
		d.Threshold = 1
	}
	if d.WindowSecs < 1 {
		d.WindowSecs = 60
	}
	if d.Action != "kvasir" {
		d.Action = "notify"
	}
	_, err := s.db.ExecContext(r.Context(),
		`UPDATE log_watchers SET server_id=?, name=?, pattern=?, threshold=?, window_secs=?, action=?, enabled=? WHERE id=?`,
		strings.TrimSpace(d.ServerID), d.Name, d.Pattern, d.Threshold, d.WindowSecs, d.Action, boolToInt(d.Enabled), id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "watcher.update", "watcher:"+id, nil)
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteWatcher(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM log_watchers WHERE id=?", id)
	s.auditLog(r, "watcher.delete", "watcher:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
