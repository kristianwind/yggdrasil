package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/llm"
)

// Rune-declared watchers + Kvasir watcher suggestions — the "works out of the
// box" half of the proactive log layer. A rune's watchers: block encodes what
// its app's log looks like when something is wrong; Kvasir suggestions cover
// everything the rune author didn't foresee, from the server's actual log.

// syncRuneWatchers seeds a server with its rune's declared watchers. Idempotent
// and additive: a rune watcher is inserted only when no rune-sourced watcher of
// that name exists for the server, so rules the user edited or disabled stay
// exactly as they left them. (Deleting a rune watcher and reinstalling restores
// the rune's default — same contract as other rune-provided defaults.)
func (s *Server) syncRuneWatchers(ctx context.Context, serverID string, gs *gameskill.Gameskill) {
	if gs == nil || len(gs.Watchers) == 0 {
		return
	}
	for _, w := range gs.Watchers {
		var exists int
		err := s.db.QueryRowContext(ctx,
			"SELECT 1 FROM log_watchers WHERE server_id=? AND name=? AND source='rune'",
			serverID, w.Name).Scan(&exists)
		if err == nil {
			continue // already seeded (possibly edited) — leave it alone
		}
		thr := w.Threshold
		if thr < 1 {
			thr = 1
		}
		win := w.WindowSecs
		if win < 1 {
			win = 60
		}
		action := w.Action
		if action != "kvasir" {
			action = "notify"
		}
		s.db.ExecContext(ctx,
			`INSERT INTO log_watchers (id, server_id, name, pattern, threshold, window_secs, action, enabled, source)
			 VALUES (?,?,?,?,?,?,?,1,'rune')`,
			uuid.New().String(), serverID, w.Name, w.Pattern, thr, win, action)
	}
}

// ---- Kvasir "suggest watchers" ----

// handleSuggestWatchers asks the configured AI for watcher rules tailored to one
// server, from its rune identity plus a tail of its actual log. Proposals are
// hard-validated (regex must compile, bounds clamped) and only returned — the
// admin adds each one explicitly through the normal create endpoint.
func (s *Server) handleSuggestWatchers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var name, skillID, containerID string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT name, gameskill_id, COALESCE(container_id,'') FROM servers WHERE id=?", id).
		Scan(&name, &skillID, &containerID); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	cfg := s.loadAIConfig(r.Context())
	if !cfg.Enabled || cfg.APIKey == "" {
		jsonError(w, "configure an AI provider in Settings → Kvasir to use suggestions", http.StatusBadRequest)
		return
	}
	var skillName, skillCategory string
	s.db.QueryRowContext(r.Context(), "SELECT name, category FROM gameskills WHERE id=?", skillID).
		Scan(&skillName, &skillCategory) //nolint:errcheck
	// The log tail grounds the suggestions in reality; without a container (never
	// started) Kvasir still knows the app type and can propose the classics.
	var tail string
	if containerID != "" {
		lines := s.watcherLogLines(containerID, "6h")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
		for i, l := range lines {
			lines[i] = stripANSI(l)
		}
		tail = strings.Join(lines, "\n")
		if len(tail) > 8000 {
			tail = tail[len(tail)-8000:]
		}
	}
	existing := []string{}
	for _, lw := range s.watchersFor(id) {
		existing = append(existing, lw.Name+" /"+lw.Pattern+"/")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := llm.Complete(ctx,
		llm.Config{Provider: cfg.Provider, Model: cfg.Model, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey},
		watcherSuggestMessages(name, skillName, skillCategory, tail, existing), 1200)
	if err != nil {
		jsonError(w, "the AI request failed or timed out", http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"suggestions": parseWatcherSuggestions(out, existing)})
}

// watcherSuggestMessages builds the suggestion prompt. Pure + testable.
func watcherSuggestMessages(serverName, skillName, skillCategory, logTail string, existing []string) []llm.Message {
	system := "You are Kvasir, the ops assistant of a game/app server panel. Propose log WATCHERS for one " +
		"server: rules of the form \"if regex PATTERN matches at least THRESHOLD lines within WINDOW_SECS " +
		"seconds of the container log, alert\". Ground them in the app type and the provided log sample — " +
		"patterns must match how THIS log actually formats errors. Focus on real trouble: crashes, fatal " +
		"errors, failed-login bursts, resource exhaustion, database errors, player-abuse markers. Avoid " +
		"patterns that would match routine lines. Use Go (RE2) regex syntax — no lookaheads or backreferences. " +
		"Respond with ONLY a JSON array, no prose or fences, at most 5 entries:\n" +
		`[{"name":"<short rule name>","pattern":"<regex>","threshold":3,"window_secs":120,` +
		`"action":"notify|kvasir","reason":"<one sentence: what this catches>"}]` + "\n" +
		"action kvasir = also wake the AI to explain/propose a fix; use it for the serious ones. " +
		"Do not duplicate the existing watchers you are given. If nothing useful can be added, return []."
	user := fmt.Sprintf("Server: %s\nRune (app type): %s (category %s)\n", serverName, skillName, skillCategory)
	if len(existing) > 0 {
		user += "Existing watchers (do not duplicate):\n- " + strings.Join(existing, "\n- ") + "\n"
	}
	if strings.TrimSpace(logTail) != "" {
		user += "Recent log sample:\n```\n" + logTail + "\n```"
	} else {
		user += "No log output yet — suggest from the app type alone."
	}
	return []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
}

type watcherSuggestion struct {
	Name       string `json:"name"`
	Pattern    string `json:"pattern"`
	Threshold  int    `json:"threshold"`
	WindowSecs int    `json:"window_secs"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
}

// parseWatcherSuggestions tolerantly extracts the JSON array and keeps only
// proposals that survive the same validation the create endpoint applies: the
// regex must compile, bounds are clamped, the action normalized, and names that
// duplicate an existing watcher are dropped. The model can never smuggle in a
// rule the admin couldn't have typed by hand.
func parseWatcherSuggestions(out string, existing []string) []watcherSuggestion {
	if i := strings.Index(out, "["); i >= 0 {
		if j := strings.LastIndex(out, "]"); j > i {
			out = out[i : j+1]
		}
	}
	var raw []watcherSuggestion
	json.Unmarshal([]byte(out), &raw) //nolint:errcheck
	have := map[string]bool{}
	for _, e := range existing {
		if n, _, ok := strings.Cut(e, " /"); ok {
			have[strings.ToLower(n)] = true
		}
	}
	kept := []watcherSuggestion{}
	for _, sg := range raw {
		sg.Name = strings.TrimSpace(sg.Name)
		sg.Pattern = strings.TrimSpace(sg.Pattern)
		if sg.Name == "" || sg.Pattern == "" || have[strings.ToLower(sg.Name)] {
			continue
		}
		if _, err := regexp.Compile(sg.Pattern); err != nil {
			continue
		}
		if sg.Threshold < 1 {
			sg.Threshold = 1
		}
		if sg.WindowSecs < 1 {
			sg.WindowSecs = 60
		}
		if sg.WindowSecs > watcherMaxWindow {
			sg.WindowSecs = watcherMaxWindow
		}
		if sg.Action != "kvasir" {
			sg.Action = "notify"
		}
		have[strings.ToLower(sg.Name)] = true
		kept = append(kept, sg)
		if len(kept) == 5 {
			break
		}
	}
	return kept
}
