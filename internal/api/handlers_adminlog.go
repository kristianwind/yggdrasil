package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Admin-log feed: a parsed, rune-declared view of a game's admin/activity log
// (joins, disconnects, deaths, kills). The rune declares a data-dir glob + a set
// of per-line classification regexps; the generic feed renders whatever it
// declares, so DayZ (.ADM) is just the first game to fill the fields. This is the
// deterministic session-history layer the AI digest will later summarize.

const adminLogTailBytes = 256 * 1024 // read at most the last 256 KB of the log
const adminLogMaxEvents = 300        // return at most this many parsed events

type adminLogEvent struct {
	Time   string `json:"time,omitempty"`
	Type   string `json:"type"`
	Player string `json:"player,omitempty"`
	Line   string `json:"line"`
}

// parseAdminLog classifies each line of an admin log against the rune's event
// rules, returning matched events in file order. The first rule that matches a
// line wins; unmatched lines (headers, chat, noise) are dropped.
func parseAdminLog(content string, cfg *gameskill.AdminLog) []adminLogEvent {
	var timeRe *regexp.Regexp
	if cfg.TimeRegex != "" {
		timeRe, _ = regexp.Compile(cfg.TimeRegex)
	}
	type rule struct {
		typ     string
		re      *regexp.Regexp
		nameIdx int
	}
	var rules []rule
	for _, e := range cfg.Events {
		re, err := regexp.Compile(e.Regex)
		if err != nil {
			continue
		}
		nameIdx := -1
		for i, n := range re.SubexpNames() {
			if n == "name" {
				nameIdx = i
			}
		}
		rules = append(rules, rule{e.Type, re, nameIdx})
	}

	events := []adminLogEvent{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, ru := range rules {
			m := ru.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			ev := adminLogEvent{Type: ru.typ, Line: strings.TrimSpace(line)}
			if ru.nameIdx >= 0 && ru.nameIdx < len(m) {
				ev.Player = strings.TrimSpace(m[ru.nameIdx])
			}
			if timeRe != nil {
				ev.Time = extractTime(timeRe, line)
			}
			events = append(events, ev)
			break // first matching rule wins
		}
	}
	return events
}

// extractTime pulls a timestamp from a line using the rune's time_regex — the
// named `time` group if present, else the whole match.
func extractTime(re *regexp.Regexp, line string) string {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	for i, n := range re.SubexpNames() {
		if n == "time" && i < len(m) {
			return strings.TrimSpace(m[i])
		}
	}
	return strings.TrimSpace(m[0])
}

// readTail returns up to the last max bytes of a file (whole file if smaller),
// dropping any partial first line so parsing starts on a clean line boundary.
func readTail(path string, max int64) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		b = b[int64(len(b))-max:]
		if nl := bytes.IndexByte(b, '\n'); nl >= 0 && nl+1 < len(b) {
			b = b[nl+1:] // skip the truncated partial line
		}
	}
	return string(b), nil
}

// newestAdminLog resolves the rune's glob under the data dir (jailed) and returns
// the most recently modified matching file.
func (s *Server) newestAdminLog(dataDir, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(dataDir, pattern))
	var newest string
	var newestMod int64
	for _, m := range matches {
		rel, err := filepath.Rel(dataDir, m)
		if err != nil {
			continue
		}
		safe, ok := safeJoin(dataDir, rel)
		if !ok || safe == dataDir {
			continue
		}
		info, err := os.Stat(safe)
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().UnixNano() > newestMod {
			newestMod = info.ModTime().UnixNano()
			newest = safe
		}
	}
	return newest
}

type adminLogResponse struct {
	Supported bool            `json:"supported"`
	File      string          `json:"file,omitempty"`
	Events    []adminLogEvent `json:"events"`
}

func (s *Server) handleAdminLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.AdminLog == nil {
		jsonOK(w, adminLogResponse{Supported: false, Events: []adminLogEvent{}})
		return
	}
	resp := adminLogResponse{Supported: true, Events: []adminLogEvent{}}
	file := s.newestAdminLog(srv.DataDir, rt.gs.AdminLog.Path)
	if file == "" {
		jsonOK(w, resp) // no log yet (server never started / no matching file)
		return
	}
	resp.File = filepath.Base(file)
	content, err := readTail(file, adminLogTailBytes)
	if err != nil {
		jsonOK(w, resp)
		return
	}
	events := parseAdminLog(content, rt.gs.AdminLog)
	// Newest first (reverse file order), capped.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if len(events) > adminLogMaxEvents {
		events = events[:adminLogMaxEvents]
	}
	resp.Events = events
	jsonOK(w, resp)
}
