package api

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Player session history. Each metrics tick, for every running server whose rune
// declares session_join/session_leave patterns, we read the log lines since we
// last looked and turn join/leave events into rows in player_sessions — a name +
// joined_at (+ left_at). This makes "who was on yesterday / when did X last play"
// answerable long after the live container log has scrolled away, and gives
// Kvasir a structured, low-injection-risk source (unlike raw search_logs).
//
// Docker's own per-line receive timestamp (Timestamps: true) is used as the
// event time, so it works regardless of each game's log date format.

// sessionCursor tracks, per server, the newest log timestamp we've processed, so
// each tick only reads new lines. In-memory: on restart we resume from a short
// window, and the DB (not this map) holds the authoritative open/closed state.
var sessionCursor sync.Map // serverID -> RFC3339 timestamp string

// sampleSessions records join/leave events for every running server that opts in
// via its rune. Called from the metrics sampler tick.
func (s *Server) sampleSessions() {
	defer recoverLog("sampleSessions")
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
		rt, err := s.loadRuntime(context.Background(), x.id)
		if err != nil || rt.gs.Players == nil || rt.gs.Players.SessionJoin == "" {
			continue
		}
		s.recordSessions(x.id, x.cid, rt.gs.Players.SessionJoin, rt.gs.Players.SessionLeave)
	}
}

// recordSessions reads the log since the last cursor and applies join/leave
// events. Idempotent across ticks: a join for a name already online is ignored,
// and a leave with no open session is a no-op, so reprocessing the boundary line
// (Docker's Since is inclusive) does no harm.
func (s *Server) recordSessions(serverID, cid, joinRe, leaveRe string) {
	jr, err := regexp.Compile(joinRe)
	if err != nil {
		return
	}
	var lr *regexp.Regexp
	if leaveRe != "" {
		if lr, err = regexp.Compile(leaveRe); err != nil {
			lr = nil
		}
	}

	since := "10m" // first look after a (re)start: just the recent window
	if v, ok := sessionCursor.Load(serverID); ok {
		since = v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rc, err := s.docker.LogsExport(ctx, cid, docker.LogExportOptions{Since: since, Timestamps: true, Tail: "all"})
	if err != nil {
		return
	}
	defer rc.Close()
	var buf bytes.Buffer
	docker.DemuxCopy(&buf, io.LimitReader(rc, 8<<20)) //nolint:errcheck // a partial read still yields whole lines

	newest := ""
	for _, line := range strings.Split(buf.String(), "\n") {
		kind, name, ts := matchSessionEvent(line, jr, lr)
		if ts != "" && ts > newest {
			newest = ts
		}
		switch kind {
		case "join":
			s.openSession(serverID, name, ts)
		case "leave":
			s.closeSession(serverID, name, ts)
		}
	}
	if newest != "" {
		sessionCursor.Store(serverID, newest)
	}
}

// matchSessionEvent parses one timestamped log line ("<rfc3339> <message>") and
// classifies it as a join/leave via the rune's regexps (capture group 1 = name).
// Pure + testable. kind is "" when the line is neither (ts is still returned so
// the caller can advance its cursor).
func matchSessionEvent(line string, jr, lr *regexp.Regexp) (kind, name, ts string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return "", "", ""
	}
	sp := strings.SplitN(line, " ", 2)
	if len(sp) != 2 {
		return "", "", ""
	}
	ts, msg := sp[0], sp[1]
	if _, e := time.Parse(time.RFC3339Nano, ts); e != nil {
		return "", "", "" // not a timestamped line
	}
	if m := jr.FindStringSubmatch(msg); len(m) > 1 {
		return "join", strings.TrimSpace(m[1]), ts
	}
	if lr != nil {
		if m := lr.FindStringSubmatch(msg); len(m) > 1 {
			return "leave", strings.TrimSpace(m[1]), ts
		}
	}
	return "", "", ts
}

// openSession starts a session unless the player already has one open (so a
// re-seen join line doesn't create duplicates).
func (s *Server) openSession(serverID, name, rfcTS string) {
	if name == "" {
		return
	}
	var existing string
	err := s.db.QueryRow(
		"SELECT id FROM player_sessions WHERE server_id=? AND player_name=? AND left_at IS NULL ORDER BY joined_at DESC LIMIT 1",
		serverID, name).Scan(&existing)
	if err == nil && existing != "" {
		return
	}
	s.db.Exec("INSERT INTO player_sessions (id, server_id, player_name, joined_at) VALUES (?,?,?,?)",
		uuid.New().String(), serverID, name, sessionTS(rfcTS))
}

// closeSession sets left_at on the player's most recent open session (no-op if
// none is open).
func (s *Server) closeSession(serverID, name, rfcTS string) {
	if name == "" {
		return
	}
	s.db.Exec(`UPDATE player_sessions SET left_at=?
		WHERE id = (SELECT id FROM player_sessions WHERE server_id=? AND player_name=? AND left_at IS NULL ORDER BY joined_at DESC LIMIT 1)`,
		sessionTS(rfcTS), serverID, name)
}

// sessionTS converts a Docker RFC3339 log timestamp to SQLite's UTC datetime
// format so it compares correctly with datetime('now').
func sessionTS(rfc string) string {
	if t, err := time.Parse(time.RFC3339Nano, rfc); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}
