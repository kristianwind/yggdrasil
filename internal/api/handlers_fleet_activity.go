package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
)

// handleFleetActivity is the fleet-wide counterpart to handleServerActivity: one
// merged timeline of everything the panel recorded about itself, across every
// server, so "what is happening right now" is a glance rather than a tour of four
// views.
//
// It is also the first UI the alerts table has ever had. Lag 1 records every
// detection there — including the ones the policy deliberately handled quietly —
// but until now the only way to see them was the daily ops digest, which means a
// detector that silently stopped working and a genuinely quiet fleet looked
// exactly the same. Suppressed rows are shown here on purpose, marked as routine,
// for precisely that reason.
//
// Admin-gated: it merges the audit log and every server's history regardless of
// per-server permissions, so it must not be reachable by someone who can only see
// one server.
func (s *Server) handleFleetActivity(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 720 {
			hours = n
		}
	}
	win := fmt.Sprintf("-%d hours", hours)

	// One shape for every source. Kind drives the icon, class drives the emphasis:
	// the UI must be able to show a routine detection without making it look like
	// an incident, since most of what lands here is routine by design.
	type item struct {
		TS       string `json:"ts"`
		Kind     string `json:"kind"`  // alert | crash | kvasir | audit
		Class    string `json:"class"` // incident | routine | "" when not applicable
		ServerID string `json:"server_id"`
		Server   string `json:"server"` // friendly name; "" for host-level entries
		Title    string `json:"title"`
		Detail   string `json:"detail"`
		Actor    string `json:"actor"` // username, "kvasir", "" when nobody did it
		Paged    bool   `json:"paged"` // an alert that actually reached a human
	}
	resp := struct {
		Items []item `json:"items"`
		Hours int    `json:"hours"`
	}{Items: []item{}, Hours: hours}

	// Every timestamp goes through datetime() so the sources sort against each
	// other. audit_log stores RFC3339 with a T and a Z; the others use SQLite's
	// own "2026-08-06 15:30:00". Compared raw, a space sorts before a T and the
	// merged timeline is quietly wrong — the same trap handleServerActivity hit.

	// Detections. sources/hits are what make an alert actionable at a glance:
	// "one address, 400 hits" and "56 addresses, 400 hits" need opposite responses.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(a.created_at), a.server_id, COALESCE(sv.name,''), a.class,
		        a.title, a.reason, a.sources, a.hits, a.paged
		   FROM alerts a LEFT JOIN servers sv ON sv.id = a.server_id
		  WHERE datetime(a.created_at) >= datetime('now', ?)
		  ORDER BY datetime(a.created_at) DESC LIMIT 200`, win); err == nil {
		for rows.Next() {
			var it item
			var reason, sources string
			var hits, paged int
			if rows.Scan(&it.TS, &it.ServerID, &it.Server, &it.Class,
				&it.Title, &reason, &sources, &hits, &paged) == nil {
				it.Kind = "alert"
				it.Paged = paged == 1
				it.Detail = reason
				if hits > 0 {
					it.Detail += fmt.Sprintf(" · %d hits", hits)
				}
				if sources != "" {
					it.Detail += " · " + sources
				}
				resp.Items = append(resp.Items, it)
			}
		}
		rows.Close()
	}

	// Exits nobody asked for.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(c.ts), c.server_id, COALESCE(sv.name,''), c.exit_code
		   FROM server_crashes c LEFT JOIN servers sv ON sv.id = c.server_id
		  WHERE datetime(c.ts) >= datetime('now', ?)
		  ORDER BY datetime(c.ts) DESC LIMIT 100`, win); err == nil {
		for rows.Next() {
			var it item
			var code int
			if rows.Scan(&it.TS, &it.ServerID, &it.Server, &code) == nil {
				it.Kind = "crash"
				it.Title = "Crashed"
				it.Detail = fmt.Sprintf("exit %d", code)
				resp.Items = append(resp.Items, it)
			}
		}
		rows.Close()
	}

	// What Kvasir made of things, and what it proposed or did about it. server_id
	// is '' for host-level events, which is why the join is LEFT and the name is
	// allowed to be empty rather than dropping the row.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(k.ts), k.server_id, COALESCE(sv.name,''), k.event,
		        k.detail, k.explanation, k.action
		   FROM kvasir_events k LEFT JOIN servers sv ON sv.id = k.server_id
		  WHERE datetime(k.ts) >= datetime('now', ?)
		  ORDER BY datetime(k.ts) DESC LIMIT 100`, win); err == nil {
		for rows.Next() {
			var it item
			var event, detail, explanation, action string
			if rows.Scan(&it.TS, &it.ServerID, &it.Server, &event,
				&detail, &explanation, &action) == nil {
				it.Kind = "kvasir"
				it.Actor = "kvasir"
				it.Title = event
				if detail != "" {
					it.Title += " — " + detail
				}
				it.Detail = explanation
				if action != "" && action != "none" {
					it.Detail += " (proposed: " + action + ")"
				}
				resp.Items = append(resp.Items, it)
			}
		}
		rows.Close()
	}

	// Who did what. Deliberately not limited to server: entries — a settings
	// change or a watcher being deleted is exactly the kind of thing you want to
	// find next to the alerts that stopped arriving afterwards.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(a.ts), COALESCE(a.action,''), COALESCE(a.username,''),
		        COALESCE(a.resource,''), COALESCE(sv.name,'')
		   FROM audit_log a
		   LEFT JOIN servers sv ON 'server:' || sv.id = a.resource
		  WHERE datetime(a.ts) >= datetime('now', ?)
		  ORDER BY datetime(a.ts) DESC LIMIT 200`, win); err == nil {
		for rows.Next() {
			var it item
			var resource string
			if rows.Scan(&it.TS, &it.Title, &it.Actor, &resource, &it.Server) == nil {
				it.Kind = "audit"
				if len(resource) > 7 && resource[:7] == "server:" {
					it.ServerID = resource[7:]
				} else {
					it.Detail = resource
				}
				resp.Items = append(resp.Items, it)
			}
		}
		rows.Close()
	}

	// Four sources, one timeline. Capped after merging so a noisy source cannot
	// crowd the others out of the window.
	sort.SliceStable(resp.Items, func(i, j int) bool { return resp.Items[i].TS > resp.Items[j].TS })
	if len(resp.Items) > 200 {
		resp.Items = resp.Items[:200]
	}

	jsonOK(w, resp)
}
