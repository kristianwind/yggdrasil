package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// releaseTagRe bounds the version passed to the root update helper.
var releaseTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

const (
	updateHelper   = "/usr/local/bin/yggdrasil-update"
	updatePathUnit = "/etc/systemd/system/yggdrasil-update.path"
)

// diskUsage returns free and total bytes for the filesystem holding path.
// Works on Linux (production) and darwin (dev): Bsize differs in type, hence the
// explicit conversion.
func diskUsage(path string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return st.Bavail * bsize, st.Blocks * bsize
}

// startDiskMonitor periodically checks free disk on the data filesystem and
// sends one notification when it crosses below 10%, re-arming once it recovers.
func (s *Server) startDiskMonitor() {
	go func() {
		path := filepath.Dir(s.cfg.Database.Path)
		alerted := false
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		check := func() {
			free, total := diskUsage(path)
			if total == 0 {
				return
			}
			pct := float64(free) / float64(total) * 100
			if pct < 10 && !alerted {
				alerted = true
				s.notifyAll("⚠️ Low disk space: " + strconv.FormatFloat(pct, 'f', 1, 64) +
					"% free on the Yggdrasil data volume.")
			} else if pct >= 15 {
				alerted = false
			}
		}
		check()
		for range t.C {
			check()
		}
	}()
}

type auditEntry struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Detail   string `json:"detail"`
	IP       string `json:"ip"`
	TS       string `json:"ts"`
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, COALESCE(username,''), action, COALESCE(resource,''),
		        COALESCE(detail_json,''), COALESCE(ip,''), ts
		 FROM audit_log ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []auditEntry{}
	for rows.Next() {
		var e auditEntry
		if err := rows.Scan(&e.ID, &e.Username, &e.Action, &e.Resource, &e.Detail, &e.IP, &e.TS); err != nil {
			continue
		}
		list = append(list, e)
	}
	jsonOK(w, list)
}

// handleVersion reports the build version + repo URL, and whether a newer
// GitHub release exists. Public (not sensitive) so the UI can show it in the
// sidebar without an extra authenticated round-trip.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := s.version
	if v == "" {
		v = "dev"
	}
	latest := s.latestRelease()
	updAvail := v != "dev" && latest != "" && semverLess(v, latest)
	jsonOK(w, map[string]any{
		"version":          v,
		"repo":             "https://github.com/kristianwind/yggdrasil",
		"latest":           latest,
		"update_available": updAvail,
		// The panel can update itself only for a released (vX.Y.Z) build with the
		// decoupled updater installed; otherwise the UI points at manual steps.
		"can_self_update": updAvail && strings.HasPrefix(v, "v") && s.selfUpdateAvailable(),
	})
}

// latestRelease returns the latest GitHub release tag, cached for 6h so the
// version endpoint (hit on every page load) doesn't hammer the GitHub API.
func (s *Server) latestRelease() string {
	s.latestMu.Lock()
	defer s.latestMu.Unlock()
	if s.latestVer != "" && time.Since(s.latestAt) < 6*time.Hour {
		return s.latestVer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/kristianwind/yggdrasil/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s.latestVer
	}
	defer resp.Body.Close()
	var body struct {
		TagName string `json:"tag_name"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.TagName != "" {
		s.latestVer = body.TagName
		s.latestAt = time.Now()
	}
	return s.latestVer
}

var (
	errAlreadyLatest = errors.New("already on the latest version")
	errHelperMissing = errors.New("in-panel update isn't enabled on this host — re-run the installer (install.sh) once to add the update helper")
	errNotReleased   = errors.New("in-panel update is only available for released builds")
)

func (s *Server) dataDir() string {
	if d := filepath.Dir(s.cfg.Database.Path); d != "" && d != "." {
		return d
	}
	return "/var/lib/yggdrasil"
}

// selfUpdateAvailable reports whether the decoupled updater is installed (the
// root helper + the systemd path unit that watches the request file).
func (s *Server) selfUpdateAvailable() bool {
	if _, err := os.Stat(updateHelper); err != nil {
		return false
	}
	_, err := os.Stat(updatePathUnit)
	return err == nil
}

// selfUpdate triggers an update to the latest official release. The panel is
// sandboxed (NoNewPrivileges + ProtectSystem) so it cannot escalate itself;
// instead it drops the target tag into a request file in its (read-write) state
// dir, which a systemd path unit picks up and runs the root oneshot updater on.
// Returns the target tag. Shared by the manual endpoint and the auto-updater.
func (s *Server) selfUpdate() (string, error) {
	cur := s.version
	if !strings.HasPrefix(cur, "v") {
		return "", errNotReleased
	}
	target := s.latestRelease()
	if target == "" {
		return "", fmt.Errorf("could not determine the latest release")
	}
	if !semverLess(cur, target) {
		return "", errAlreadyLatest
	}
	if !releaseTagRe.MatchString(target) {
		return "", fmt.Errorf("unexpected release tag format")
	}
	if !s.selfUpdateAvailable() {
		return "", errHelperMissing
	}
	req := filepath.Join(s.dataDir(), ".update-request")
	if err := os.WriteFile(req, []byte(target+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("could not trigger the updater: %v", err)
	}
	return target, nil
}

// handleUpdateStatus surfaces the last update result written by the oneshot
// helper, so the UI can show a checksum/download failure after a restart poll
// times out.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(s.dataDir(), ".update-status"))
	if err != nil {
		jsonOK(w, map[string]any{"state": "idle"})
		return
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil {
		st = map[string]any{"state": "unknown"}
	}
	jsonOK(w, st)
}

// handleSystemUpdate updates the panel to the latest official release (admin).
// Runs the helper synchronously so download/checksum failures surface in the
// response; the helper schedules the restart a couple of seconds out, after we
// reply, so the client can poll /api/version.
func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	from := s.version
	target, err := s.selfUpdate()
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, errAlreadyLatest), errors.Is(err, errNotReleased):
			code = http.StatusBadRequest
		case errors.Is(err, errHelperMissing):
			code = http.StatusServiceUnavailable
		}
		jsonError(w, err.Error(), code)
		return
	}
	s.auditLog(r, "system.update", "version:"+target, map[string]string{"from": from})
	jsonOK(w, map[string]any{"status": "updating", "from": from, "to": target})
}

func autoUpdateHour(v string) int {
	if h, err := strconv.Atoi(v); err == nil && h >= 0 && h <= 23 {
		return h
	}
	return 4
}

// handleCheckUpdate forces a fresh GitHub release check (bypassing the 6h cache)
// and returns the current version info, so the UI's "Check now" button reflects
// a just-published release immediately.
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	s.latestMu.Lock()
	s.latestAt = time.Time{} // invalidate the cache so latestRelease refetches
	s.latestMu.Unlock()
	latest := s.latestRelease()
	v := s.version
	if v == "" {
		v = "dev"
	}
	updAvail := v != "dev" && latest != "" && semverLess(v, latest)
	jsonOK(w, map[string]any{
		"version":          v,
		"repo":             "https://github.com/kristianwind/yggdrasil",
		"latest":           latest,
		"update_available": updAvail,
		"can_self_update":  updAvail && strings.HasPrefix(v, "v") && s.selfUpdateAvailable(),
	})
}

// handleGetAutoUpdate / handleSetAutoUpdate manage the opt-in scheduled updater.
func (s *Server) handleGetAutoUpdate(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"enabled": s.getSetting(r.Context(), "auto_update_enabled") == "1",
		"hour":    autoUpdateHour(s.getSetting(r.Context(), "auto_update_hour")),
	})
}

func (s *Server) handleSetAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
		Hour    int  `json:"hour"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Hour < 0 || req.Hour > 23 {
		req.Hour = 4
	}
	s.setSetting(r.Context(), "auto_update_enabled", boolStr(req.Enabled))
	s.setSetting(r.Context(), "auto_update_hour", strconv.Itoa(req.Hour))
	s.auditLog(r, "settings.auto_update", "", map[string]any{"enabled": req.Enabled, "hour": req.Hour})
	jsonOK(w, map[string]any{"enabled": req.Enabled, "hour": req.Hour})
}

// autoUpdateLoop applies releases when opt-in auto-update is on. The hour gate +
// per-day marker make it act at most once a day at the chosen server-local hour.
// A self-update restarts only the panel binary — game/app containers keep
// running — so the impact is a few seconds of UI downtime.
func (s *Server) autoUpdateLoop() {
	t := time.NewTicker(20 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.maybeAutoUpdate()
	}
}

func (s *Server) maybeAutoUpdate() {
	ctx := context.Background()
	if s.getSetting(ctx, "auto_update_enabled") != "1" {
		return
	}
	now := time.Now()
	if now.Hour() != autoUpdateHour(s.getSetting(ctx, "auto_update_hour")) {
		return
	}
	today := now.Format("2006-01-02")
	if s.getSetting(ctx, "auto_update_last") == today {
		return
	}
	if !strings.HasPrefix(s.version, "v") {
		return
	}
	latest := s.latestRelease()
	if latest == "" || !semverLess(s.version, latest) {
		return
	}
	s.setSetting(ctx, "auto_update_last", today) // mark before attempting — no retry storms
	log.Printf("yggdrasil: auto-update %s -> %s", s.version, latest)
	if _, err := s.selfUpdate(); err != nil {
		log.Printf("yggdrasil: auto-update failed: %v", err)
	}
}


// semverLess reports whether version a is older than b. Tags look like vMAJOR.
// MINOR.PATCH; unparsable parts compare as 0.
func semverLess(a, b string) bool {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	dockerOK := s.docker.Ping(r.Context()) == nil

	var serverCount, runningCount, userCount, gameskillCount int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM servers").Scan(&serverCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM servers WHERE status='running'").Scan(&runningCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM gameskills").Scan(&gameskillCount)

	free, total := diskUsage(filepath.Dir(s.cfg.Database.Path))
	memTotal, memUsed := hostMem()
	cpuPct := hostCPUPercent()

	jsonOK(w, map[string]interface{}{
		"docker_ok":        dockerOK,
		"servers":          serverCount,
		"servers_running":  runningCount,
		"users":            userCount,
		"gameskills":       gameskillCount,
		"go_version":       runtime.Version(),
		"arch":             runtime.GOARCH,
		"cpu_count":        runtime.NumCPU(),
		"cpu_percent":      cpuPct, // -1 when unavailable (e.g. non-Linux)
		"mem_total_bytes":  memTotal,
		"mem_used_bytes":   memUsed,
		"disk_free_bytes":  free,
		"disk_total_bytes": total,
	})
}
