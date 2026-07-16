package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Log export: download a server's console output, or an install's, as a text
// file.
//
// The two are different animals and the UI says so, because pretending otherwise
// would promise history that isn't there:
//
//   - The console log belongs to Docker. It supports real filtering (a line
//     count, a time range), but it starts when the current container was
//     created — Yggdrasil recreates the container on every start and restart, so
//     a restart clears it. That's why the ranges are relative to now and there is
//     no date picker: asking for last Tuesday would always come back empty.
//   - The install log is a 500-line in-memory ring, not persisted. It holds the
//     most recent install for as long as the panel has been up.

// sinceRe accepts what Docker's `since`/`until` take: a duration like "90m", or
// an RFC3339 timestamp. Anything else is refused rather than passed through —
// these reach the Docker API, and a value we don't understand is a bug we'd
// rather see than forward.
var sinceRe = regexp.MustCompile(`^(\d+[smhd]|\d{4}-\d{2}-\d{2}T[\d:.+Z-]+)$`)

const maxExportTailLines = 500000 // a ceiling, not a target; see parseTail

// parseTail validates a line count. Docker takes "all" or a number.
func parseTail(v string) (string, error) {
	if v == "" || v == "all" {
		return "all", nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("tail must be a positive number of lines, or \"all\"")
	}
	if n > maxExportTailLines {
		n = maxExportTailLines
	}
	return strconv.Itoa(n), nil
}

func parseSince(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !sinceRe.MatchString(v) {
		return "", fmt.Errorf("expected a duration like 30m, 2h or 7d, or an RFC3339 timestamp")
	}
	return v, nil
}

// logFilename builds a download name that says what it is without needing to be
// opened: server, kind, and when it was taken.
func logFilename(serverName, kind string, now time.Time) string {
	base := slugName(serverName)
	if base == "" {
		base = "server"
	}
	return fmt.Sprintf("%s-%s-%s.log", base, kind, now.Format("20060102-150405"))
}

// handleExportServerLogs streams a slice of the container's log as a download.
//
// Read-only, so it matches the log stream's own gate (server.view) rather than
// the interactive console's.
func (s *Server) handleExportServerLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	// Validate the request before looking at the server's state: a malformed
	// `since` is wrong whether or not there's a container to apply it to, and
	// answering "no container" would hide the typo until they'd fixed the other
	// thing.
	q := r.URL.Query()
	tail, err := parseTail(q.Get("tail"))
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	since, err := parseSince(q.Get("since"))
	if err != nil {
		jsonError(w, "since: "+err.Error(), http.StatusBadRequest)
		return
	}
	until, err := parseSince(q.Get("until"))
	if err != nil {
		jsonError(w, "until: "+err.Error(), http.StatusBadRequest)
		return
	}

	if srv.ContainerID == "" {
		jsonError(w, "this server has no container — nothing has run yet, so there is no log to export",
			http.StatusNotFound)
		return
	}

	rc, err := s.docker.LogsExport(r.Context(), srv.ContainerID, docker.LogExportOptions{
		Tail:       tail,
		Since:      since,
		Until:      until,
		Timestamps: q.Get("timestamps") == "true",
	})
	if err != nil {
		jsonError(w, "could not read the container log", http.StatusBadGateway)
		return
	}
	defer rc.Close()

	// Headers before the first byte: once DemuxCopy starts writing, the status is
	// already sent and an error can only be truncated output.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", logFilename(srv.Name, "console", time.Now())))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Stream it. Docker multiplexes stdout and stderr into frames; DemuxCopy
	// collapses them into the plain text you'd see in the console.
	if err := docker.DemuxCopy(w, rc); err != nil {
		// The client has bytes already, so there's no status left to change. Log
		// it; a truncated file is the honest outcome.
		s.auditLog(r, "server.logs.export.error", "server:"+id, map[string]string{"error": err.Error()})
	}
	s.auditLog(r, "server.logs.export", "server:"+id,
		map[string]string{"tail": tail, "since": since, "until": until})
}

// handleExportInstallLog serves the in-memory install history as a download.
//
// No range options: the hub keeps the last 500 lines of the most recent install
// and nothing older exists to slice.
func (s *Server) handleExportInstallLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}

	lines := s.install.History(id)
	if len(lines) == 0 {
		jsonError(w, "no install log — either this server hasn't been installed since the panel started, or the log has been cleared",
			http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", logFilename(srv.Name, "install", time.Now())))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, strings.Join(lines, "\n"))
	if len(lines) > 0 {
		fmt.Fprintln(w)
	}
	s.auditLog(r, "server.install_log.export", "server:"+id, map[string]string{"lines": strconv.Itoa(len(lines))})
}
