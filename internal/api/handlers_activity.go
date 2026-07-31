package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// handleServerActivity surfaces the panel's own recorded history for one server —
// named player sessions (who was on, when) and notable security/health events
// (WordPress xmlrpc/login attempts, HTTP 5xx, …) — so the intelligence Kvasir can
// answer in chat is also visible in the UI. Read-only, ServerView-gated, last N
// hours (default 7 days, max 30). Both lists are empty for servers whose rune
// records neither, which the UI uses to hide the tab.
func (s *Server) handleServerActivity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	hours := 168
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 720 {
			hours = n
		}
	}
	win := fmt.Sprintf("-%d hours", hours)

	type session struct {
		Name     string `json:"name"`
		JoinedAt string `json:"joined_at"`
		LeftAt   string `json:"left_at"`
	}
	type event struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Subject string `json:"subject"`
		Count   int    `json:"count"`
		LastTS  string `json:"last_ts"`
	}
	// One server's life story: who started, stopped or restarted it and when,
	// plus the exits nobody asked for. The panel already recorded all of this,
	// but only in places you had to know to look — the fleet-wide audit log, the
	// crash list, the schedule run log — so "why did this restart last night"
	// meant cross-referencing three views. Actor is what makes it useful: a
	// scheduled restart, a Kvasir auto-fix, a Discord command and a person
	// clicking Stop all look identical afterwards otherwise.
	type lifecycle struct {
		TS     string `json:"ts"`
		Action string `json:"action"`
		Actor  string `json:"actor"`  // username, "kvasir", "schedule", "" when nobody did it
		Detail string `json:"detail"` // exit code, schedule name, "via discord"…
	}
	resp := struct {
		Sessions  []session   `json:"sessions"`
		Events    []event     `json:"events"`
		Lifecycle []lifecycle `json:"lifecycle"`
		Hours     int         `json:"hours"`
	}{Sessions: []session{}, Events: []event{}, Lifecycle: []lifecycle{}, Hours: hours}

	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT player_name, joined_at, COALESCE(left_at,'')
		 FROM player_sessions WHERE server_id=? AND joined_at >= datetime('now', ?)
		 ORDER BY joined_at DESC LIMIT 200`, id, win); err == nil {
		for rows.Next() {
			var e session
			if rows.Scan(&e.Name, &e.JoinedAt, &e.LeftAt) == nil {
				resp.Sessions = append(resp.Sessions, e)
			}
		}
		rows.Close()
	}

	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT key, label, subject, SUM(count) AS c, MAX(last_ts)
		 FROM app_events WHERE server_id=? AND bucket >= datetime('now', ?)
		 GROUP BY key, subject ORDER BY c DESC LIMIT 200`, id, win); err == nil {
		for rows.Next() {
			var e event
			if rows.Scan(&e.Key, &e.Label, &e.Subject, &e.Count, &e.LastTS) == nil {
				resp.Events = append(resp.Events, e)
			}
		}
		rows.Close()
	}

	// Anything a person (or the Discord bot, or Kvasir) did through the panel.
	// auditSystem writes here too, with the actor in `username`, so all four
	// kinds of initiator land in one place.
	//
	// datetime() on both sides, because audit_log stores ISO-8601 with a T and a
	// Z ("2026-07-31T10:54:29Z") while server_crashes and schedule_runs use
	// SQLite's own "2026-07-31 10:54:29". Compared as raw strings those are not
	// ordered against each other at all — a space sorts before a T, so every
	// crash would sink below every audit entry from the same day whatever the
	// clock said, and the merged timeline would be quietly wrong.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(ts), action, COALESCE(username,''), COALESCE(detail_json,'')
		 FROM audit_log WHERE resource=? AND datetime(ts) >= datetime('now', ?)
		 ORDER BY datetime(ts) DESC LIMIT 200`, "server:"+id, win); err == nil {
		for rows.Next() {
			var e lifecycle
			if rows.Scan(&e.TS, &e.Action, &e.Actor, &e.Detail) == nil {
				resp.Lifecycle = append(resp.Lifecycle, e)
			}
		}
		rows.Close()
	}

	// Exits nobody asked for. These have no actor by definition, which is the
	// point: an entry with no actor next to one with a name is how you tell "it
	// fell over" from "someone stopped it".
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(ts), exit_code FROM server_crashes WHERE server_id=? AND datetime(ts) >= datetime('now', ?)
		 ORDER BY ts DESC LIMIT 100`, id, win); err == nil {
		for rows.Next() {
			var ts string
			var code int
			if rows.Scan(&ts, &code) == nil {
				resp.Lifecycle = append(resp.Lifecycle, lifecycle{
					TS: ts, Action: "server.crash", Detail: fmt.Sprintf("exit %d", code),
				})
			}
		}
		rows.Close()
	}

	// Scheduled actions. Worth its own source rather than relying on the audit
	// log: a nightly update that restarts a server the operator had deliberately
	// stopped is exactly the kind of thing this view has to be able to explain,
	// and it once took a schedule_runs query to work out.
	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT datetime(ran_at), COALESCE(action,''), COALESCE(schedule_name,''), COALESCE(status,''), COALESCE(detail,'')
		 FROM schedule_runs WHERE server_id=? AND datetime(ran_at) >= datetime('now', ?)
		 ORDER BY ran_at DESC LIMIT 100`, id, win); err == nil {
		for rows.Next() {
			var ts, action, name, status, detail string
			if rows.Scan(&ts, &action, &name, &status, &detail) == nil {
				d := name
				if status != "" {
					d += " — " + status
				}
				if detail != "" {
					d += ": " + detail
				}
				resp.Lifecycle = append(resp.Lifecycle, lifecycle{
					TS: ts, Action: "schedule." + action, Actor: "schedule", Detail: d,
				})
			}
		}
		rows.Close()
	}

	// Three sources, one timeline. Newest first, and capped after merging so a
	// noisy source can't crowd the others out of the window.
	sort.SliceStable(resp.Lifecycle, func(i, j int) bool { return resp.Lifecycle[i].TS > resp.Lifecycle[j].TS })
	if len(resp.Lifecycle) > 200 {
		resp.Lifecycle = resp.Lifecycle[:200]
	}

	jsonOK(w, resp)
}
