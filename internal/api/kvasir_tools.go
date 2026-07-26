package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Kvasir read-only lookups. Mid-chat the model may request one panel data lookup
// by ending a reply with a ```lookup block; the server runs it — strictly
// read-only, and only against servers the caller already controls — and feeds
// the result back as UNTRUSTED data, never as instructions, because a value like
// a log line can contain player-authored text crafted to look like a command.

// chatMaxLookups caps how many lookups one chat turn may chain, so a model that
// keeps asking can't loop forever (and can't run up unbounded work).
const chatMaxLookups = 3

// lookupReq is one requested lookup, parsed from a ```lookup block.
type lookupReq struct {
	Tool    string `json:"tool"`    // player_history | metrics_window | list_backups | search_logs
	Server  string `json:"server"`  // exact server name from the snapshot
	Hours   int    `json:"hours"`   // window for player_history / metrics_window
	Pattern string `json:"pattern"` // case-insensitive substring for search_logs
}

// splitLookup separates the visible reply from a trailing ```lookup block and
// parses it. Mirrors splitChatActions. Pure + testable.
func splitLookup(full string) (text string, req *lookupReq) {
	marker := "```lookup"
	i := strings.Index(full, marker)
	if i < 0 {
		return strings.TrimSpace(full), nil
	}
	text = strings.TrimSpace(full[:i])
	rest := full[i+len(marker):]
	if j := strings.Index(rest, "```"); j >= 0 {
		rest = rest[:j]
	}
	var r lookupReq
	if a, b := strings.Index(rest, "{"), strings.LastIndex(rest, "}"); a >= 0 && b > a {
		if json.Unmarshal([]byte(rest[a:b+1]), &r) == nil && r.Tool != "" {
			return text, &r
		}
	}
	return text, nil
}

// runLookup executes a read-only lookup against a server the caller controls and
// returns a short plain-text result. servers is the caller's controllable set, so
// a server outside it (or unknown) is refused — the chat can't read data the user
// has no access to.
func (s *Server) runLookup(ctx context.Context, servers []serverRow, req *lookupReq) string {
	var srv *serverRow
	want := strings.ToLower(strings.TrimSpace(req.Server))
	for i := range servers {
		if strings.ToLower(servers[i].Name) == want {
			srv = &servers[i]
			break
		}
	}
	if srv == nil {
		return fmt.Sprintf("No server named %q that you manage.", req.Server)
	}
	switch req.Tool {
	case "player_history":
		return s.lookupPlayerHistory(ctx, srv, req.Hours)
	case "metrics_window":
		return s.lookupMetricsWindow(ctx, srv, req.Hours)
	case "list_backups":
		return s.lookupBackups(ctx, srv)
	case "search_logs":
		return s.lookupSearchLogs(ctx, srv, req.Pattern)
	default:
		return fmt.Sprintf("Unknown lookup %q. Available: player_history, metrics_window, list_backups, search_logs.", req.Tool)
	}
}

// clampHours keeps a requested window sane (default 24h, max 30 days).
func clampHours(h int) int {
	if h <= 0 {
		return 24
	}
	if h > 720 {
		return 720
	}
	return h
}

func (s *Server) lookupPlayerHistory(ctx context.Context, srv *serverRow, hours int) string {
	hours = clampHours(hours)
	win := fmt.Sprintf("-%d hours", hours)
	var samples, withPlayers, peak int
	var lastActive sql.NullString
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN players>0 THEN 1 ELSE 0 END),0),
		       COALESCE(MAX(players),-1), MAX(CASE WHEN players>0 THEN ts END)
		FROM metrics WHERE server_id=? AND ts >= datetime('now',?)`, srv.ID, win).
		Scan(&samples, &withPlayers, &peak, &lastActive)
	if samples == 0 {
		return fmt.Sprintf("%s: no metrics samples in the last %dh.", srv.Name, hours)
	}
	if peak < 0 {
		return fmt.Sprintf("%s: this rune doesn't report player counts, so player history isn't available.", srv.Name)
	}
	var cur sql.NullInt64
	s.db.QueryRowContext(ctx, `SELECT players FROM metrics WHERE server_id=? ORDER BY ts DESC LIMIT 1`, srv.ID).Scan(&cur)
	now := "unknown"
	if cur.Valid && cur.Int64 >= 0 {
		now = strconv.FormatInt(cur.Int64, 10)
	}
	last := "no players online at any point in the window"
	if lastActive.Valid {
		last = "last had players " + humanizeSince(lastActive.String)
	}
	return fmt.Sprintf("%s over the last %dh: peak %d players; currently %s; %d of %d samples had someone online; %s.",
		srv.Name, hours, peak, now, withPlayers, samples, last)
}

func (s *Server) lookupMetricsWindow(ctx context.Context, srv *serverRow, hours int) string {
	hours = clampHours(hours)
	win := fmt.Sprintf("-%d hours", hours)
	var n int
	var avgCPU, peakCPU, avgMem, peakMem float64
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(ROUND(AVG(cpu),1),0), COALESCE(ROUND(MAX(cpu),1),0),
		       COALESCE(ROUND(AVG(mem_mb)),0), COALESCE(ROUND(MAX(mem_mb)),0)
		FROM metrics WHERE server_id=? AND ts >= datetime('now',?)`, srv.ID, win).
		Scan(&n, &avgCPU, &peakCPU, &avgMem, &peakMem)
	if n == 0 {
		return fmt.Sprintf("%s: no metrics samples in the last %dh.", srv.Name, hours)
	}
	return fmt.Sprintf("%s over the last %dh (%d samples): CPU avg %.1f%%/peak %.1f%%; memory avg %.0f MB/peak %.0f MB.",
		srv.Name, hours, n, avgCPU, peakCPU, avgMem, peakMem)
}

func (s *Server) lookupBackups(ctx context.Context, srv *serverRow) string {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COALESCE(size_bytes,0), created_at, COALESCE(error_msg,'')
		FROM backups WHERE server_id=? ORDER BY created_at DESC LIMIT 8`, srv.ID)
	if err != nil {
		return "Couldn't read the backup list."
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var status, created, errMsg string
		var size int64
		if rows.Scan(&status, &size, &created, &errMsg) == nil {
			line := fmt.Sprintf("%s — %s, %.1f MB", created, status, float64(size)/1048576)
			if errMsg != "" {
				line += " (" + errMsg + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return fmt.Sprintf("%s: no backups recorded.", srv.Name)
	}
	return fmt.Sprintf("%s — recent backups (newest first):\n%s", srv.Name, strings.Join(lines, "\n"))
}

func (s *Server) lookupSearchLogs(ctx context.Context, srv *serverRow, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "search_logs needs a non-empty pattern."
	}
	if srv.ContainerID == "" {
		return fmt.Sprintf("%s has no running container, so there's no live log to search.", srv.Name)
	}
	rc, err := s.docker.LogsSnapshot(ctx, srv.ContainerID, "3000")
	if err != nil {
		return "Couldn't read the container log."
	}
	defer rc.Close()
	var buf bytes.Buffer
	docker.DemuxCopy(&buf, io.LimitReader(rc, 4<<20)) //nolint:errcheck // best-effort; a partial log still searches
	needle := strings.ToLower(pattern)
	var matches []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(strings.ToLower(line), needle) {
			if len(line) > 300 {
				line = line[:300] + "…"
			}
			matches = append(matches, line)
			if len(matches) >= 30 {
				break
			}
		}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No lines matching %q in the last 3000 log lines of %s.", pattern, srv.Name)
	}
	return fmt.Sprintf("%s — up to 30 log lines matching %q (newest 3000 lines searched):\n%s",
		srv.Name, pattern, strings.Join(matches, "\n"))
}
