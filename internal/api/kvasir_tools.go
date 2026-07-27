package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/query"
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

// lookupMinLevel is the chat data-access tier each tool needs: panel data at
// level 1, live logs at level 2. runLookup and the prompt both consult this so a
// tool is only offered — and only runs — when the admin has enabled its tier.
var lookupMinLevel = map[string]int{
	"player_history":  1,
	"player_sessions": 1,
	"metrics_window":  1,
	"list_backups":    1,
	"roster":          1,
	"search_logs":     2,
}

// runLookup executes a read-only lookup against a server the caller controls and
// returns a short plain-text result. servers is the caller's controllable set, so
// a server outside it (or unknown) is refused — the chat can't read data the user
// has no access to. level is the ai_config.chat_data_level tier; a tool above it
// is refused (defense in depth behind the prompt, which only advertises allowed
// tools).
func (s *Server) runLookup(ctx context.Context, servers []serverRow, req *lookupReq, level int) string {
	min, known := lookupMinLevel[req.Tool]
	if !known {
		return fmt.Sprintf("Unknown lookup %q. Available: player_history, player_sessions, metrics_window, list_backups, roster, search_logs.", req.Tool)
	}
	if level < min {
		return fmt.Sprintf("The %q lookup isn't enabled on this panel (the admin controls Kvasir's data access in Settings).", req.Tool)
	}
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
	case "player_sessions":
		return s.lookupPlayerSessions(ctx, srv, req.Hours)
	case "metrics_window":
		return s.lookupMetricsWindow(ctx, srv, req.Hours)
	case "list_backups":
		return s.lookupBackups(ctx, srv)
	case "roster":
		return s.lookupRoster(ctx, srv)
	case "search_logs":
		return s.lookupSearchLogs(ctx, srv, req.Pattern)
	}
	return fmt.Sprintf("Unknown lookup %q.", req.Tool)
}

// lookupRoster reports who is online right now via the game's own RCON list
// command or the Steam query protocol. Games that report only a count (or no
// names — e.g. Bedrock, whose names live in the log) are told to fall back to
// search_logs, which is where join/leave lines with names actually are.
func (s *Server) lookupRoster(ctx context.Context, srv *serverRow) string {
	rt, err := s.loadRuntime(ctx, srv.ID)
	if err != nil || rt.gs.Players == nil {
		return fmt.Sprintf("%s: no live roster for this game. For who joined or left, use search_logs (pattern \"connected\" or \"joined\").", srv.Name)
	}
	pl := rt.gs.Players
	if pl.ListCommand != "" {
		if out, e := s.rconExec(ctx, srv.ID, pl.ListCommand); e == nil {
			if players, perr := parsePlayers(out, pl.PlayerRegex); perr == nil && len(players) > 0 {
				names := make([]string, 0, len(players))
				for _, p := range players {
					names = append(names, p.Name)
				}
				return fmt.Sprintf("%s: %d online now — %s.", srv.Name, len(names), strings.Join(names, ", "))
			}
		}
	}
	if rt.gs.Query != nil {
		if names, qerr := query.QueryPlayers(rt.gs.Query.Type, "127.0.0.1", rt.queryPort(), 3*time.Second); qerr == nil {
			clean := make([]string, 0, len(names))
			for _, n := range names {
				if n = strings.TrimSpace(n); n != "" {
					clean = append(clean, n)
				}
			}
			if len(clean) > 0 {
				return fmt.Sprintf("%s: %d online now — %s.", srv.Name, len(clean), strings.Join(clean, ", "))
			}
			return fmt.Sprintf("%s: %d online now, but the game isn't reporting names via query. For names, use search_logs (pattern \"connected\"/\"joined\").", srv.Name, len(names))
		}
	}
	return fmt.Sprintf("%s: couldn't read a live roster right now. For who joined/left, use search_logs (pattern \"connected\"/\"joined\").", srv.Name)
}

// llmLocality reports whether the configured LLM endpoint is on this network
// (so log data fed to it stays local) or a third-party cloud (so it would leave
// the box). Used to warn the admin before they enable the log-access tier.
func llmLocality(provider, baseURL string) (local bool, host string) {
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") && strings.TrimSpace(baseURL) == "" {
		return true, "localhost" // Ollama defaults to a local daemon
	}
	b := strings.TrimSpace(baseURL)
	if b == "" {
		return false, "" // no override → a hosted default (OpenAI/Anthropic)
	}
	if !strings.Contains(b, "://") {
		b = "http://" + b
	}
	u, err := url.Parse(b)
	if err != nil {
		return false, ""
	}
	host = u.Hostname()
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".internal") || !strings.Contains(h, ".") {
		return true, host
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return true, host
		}
		// Tailscale/CGNAT 100.64.0.0/10 counts as "your network" too.
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true, host
		}
	}
	return false, host
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

// lookupPlayerSessions answers "who was on, and when" over a window from the
// recorded session history — the structured, reliable source for named player
// activity (unlike search_logs, it survives log rotation and needs no grep).
func (s *Server) lookupPlayerSessions(ctx context.Context, srv *serverRow, hours int) string {
	hours = clampHours(hours)
	win := fmt.Sprintf("-%d hours", hours)
	rows, err := s.db.QueryContext(ctx, `
		SELECT player_name, MIN(joined_at), MAX(COALESCE(left_at, joined_at)), COUNT(*)
		FROM player_sessions
		WHERE server_id=? AND joined_at >= datetime('now', ?)
		GROUP BY player_name ORDER BY MAX(joined_at) DESC LIMIT 50`, srv.ID, win)
	if err != nil {
		return "Couldn't read player sessions."
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, first, last string
		var n int
		if rows.Scan(&name, &first, &last, &n) == nil {
			line := fmt.Sprintf("%s — first %s, last %s", name, first, last)
			if n > 1 {
				line += fmt.Sprintf(" (%d sessions)", n)
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return fmt.Sprintf("%s: no recorded player sessions in the last %dh. (Session history is kept going forward from when the panel started tracking it — older activity, or a rune without join/leave patterns, won't appear here.)", srv.Name, hours)
	}
	return fmt.Sprintf("%s — named players seen in the last %dh (most recent first), times in UTC:\n%s", srv.Name, hours, strings.Join(lines, "\n"))
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
