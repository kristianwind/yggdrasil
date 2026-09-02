package api

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// The Statistics page: where the machine's CPU, RAM and disk actually go.
//
// It exists because the Dashboard's three host sparklines can only say THAT the
// box is at 93%, never WHICH of the things on it is responsible — and the
// obvious guess is wrong in a way that wastes an afternoon. Measured on the
// production box on 2026-09-02: every server's data directory together came to
// about 5 GB, while Docker was holding 157.8 GB of images, 103.8 GB of it
// reclaimable. Ranking servers by disk would have answered confidently and
// pointed at the wrong thing.
//
// So the page reports two different breakdowns and does not blur them: the fleet
// (which server uses what) and the filesystem (server data vs Docker vs
// everything else). Only the second one explains a full disk on this fleet.

// serverUsage is one row of the fleet ranking.
type serverUsage struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	CPU    float64 `json:"cpu_percent"`
	MemMB  float64 `json:"mem_mb"`
	// DiskMB is -1 when the server's data directory has not been measured yet.
	// Zero is a real answer ("an empty data dir"), so it cannot double as "unknown"
	// — a brand-new panel would otherwise report every server as using no disk,
	// which reads as a fact rather than as a missing measurement.
	DiskMB int64  `json:"disk_mb"`
	DiskTS string `json:"disk_ts"`
}

type diskBreakdown struct {
	TotalBytes int64 `json:"total_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	FreeBytes  int64 `json:"free_bytes"`

	// ServerDataBytes is the sum of the measured data directories. Measured is not
	// the same as complete — a server whose dir has never been walked contributes
	// nothing — so the count is reported alongside it rather than presenting the
	// sum as the whole truth.
	ServerDataBytes  int64  `json:"server_data_bytes"`
	ServerDataKnown  int    `json:"server_data_known"`
	ServerDataTotal  int    `json:"server_data_total"`
	ServerDataSample string `json:"server_data_sampled_at"`

	Docker      *docker.DiskSummary `json:"docker"`
	DockerError string              `json:"docker_error,omitempty"`

	// OtherBytes is what is left of the used space once server data and Docker are
	// accounted for: the OS, logs, backups, anything else on the volume. It is a
	// remainder, not a measurement, and it is clamped at zero — Docker reports
	// deduplicated layer sizes, so the parts can slightly exceed the whole and a
	// negative "other" would be an artefact rather than a finding.
	OtherBytes int64 `json:"other_bytes"`
}

type statsResponse struct {
	Host struct {
		CPUPercent float64 `json:"cpu_percent"` // -1 when unavailable (non-Linux)
		CPUCount   int     `json:"cpu_count"`
		MemUsedMB  float64 `json:"mem_used_mb"`
		MemTotalMB float64 `json:"mem_total_mb"`
	} `json:"host"`
	Disk    diskBreakdown `json:"disk"`
	Servers []serverUsage `json:"servers"`
}

// The daemon walks its own storage to answer /system/df, which on a box with
// hundreds of images is slow enough to notice. The page is allowed to poll, so
// the call is cached and the page gets a number a minute old rather than a
// spinner.
var (
	duMu  sync.Mutex
	duVal *docker.DiskSummary
	duAt  time.Time
	duTTL = 60 * time.Second
)

func (s *Server) dockerDiskUsage(ctx context.Context) (*docker.DiskSummary, error) {
	if s.docker == nil {
		return nil, nil
	}
	duMu.Lock()
	defer duMu.Unlock()
	if duVal != nil && time.Since(duAt) < duTTL {
		return duVal, nil
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	v, err := s.docker.DiskUsage(c)
	if err != nil {
		// Keep serving the last good answer if there was one: a page that loses its
		// disk breakdown because one poll timed out is worse than a slightly stale
		// one, and the timestamp is on the page anyway.
		if duVal != nil {
			return duVal, nil
		}
		return nil, err
	}
	duVal, duAt = v, time.Now()
	return v, nil
}

// pruneMu serialises prunes. Two admins pressing the button at once would each
// wait on the daemon's own lock and the second would report freeing nothing,
// which reads like a failure rather than like "somebody beat you to it".
var pruneMu sync.Mutex

// handlePruneImages frees the space the Statistics page reports as reclaimable.
//
// The page deliberately had no button at first: a prune is destructive, and the
// panel had no business triggering one. Kristian asked for the panel to help
// rather than only report, and the argument does not survive contact with what
// this particular prune does — it removes only DANGLING images, which are
// untagged, unreferenced, and impossible to start a server from, and the daemon
// refuses to remove any image a container still uses. The dangerous variant
// (`prune -a`, which also takes the images of stopped servers) is not reachable
// from here at all.
//
// Still admin-only, still audited, and still an explicit press — nothing prunes
// on a timer. Reclaiming space is cheap to do and expensive to undo, so a human
// decides when.
func (s *Server) handlePruneImages(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		jsonError(w, "docker is not available", http.StatusServiceUnavailable)
		return
	}
	pruneMu.Lock()
	defer pruneMu.Unlock()

	// Deliberately not r.Context(): a prune of a hundred images outlives the
	// browser's patience, and abandoning it halfway leaves the daemon mid-delete
	// with nobody reading the result.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deleted, freed, err := s.docker.PruneDanglingImages(ctx)
	if err != nil {
		jsonError(w, "prune failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// The cached df is now wrong by exactly what was just freed, and the page will
	// re-poll within seconds — expiring it here is the difference between the
	// number dropping and the number appearing not to have worked.
	duMu.Lock()
	duVal, duAt = nil, time.Time{}
	duMu.Unlock()

	s.auditLog(r, "system.prune_images", "", map[string]any{"deleted": deleted, "freed_bytes": freed})
	jsonOK(w, map[string]any{"deleted": deleted, "freed_bytes": freed})
}

// handleSystemStats backs the Statistics page. Admin-only: it names every server
// on the box and how much of the machine each one is using, which is more than a
// delegate is entitled to see about servers that are not theirs.
func (s *Server) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	var out statsResponse

	out.Host.CPUPercent = hostCPUPercent()
	out.Host.CPUCount = runtime.NumCPU()
	memTotal, memUsed := hostMem()
	const mb = 1024 * 1024
	out.Host.MemUsedMB = float64(memUsed) / mb
	out.Host.MemTotalMB = float64(memTotal) / mb

	free, total := diskUsage(filepath.Dir(s.cfg.Database.Path))
	out.Disk.TotalBytes = int64(total)
	out.Disk.FreeBytes = int64(free)
	out.Disk.UsedBytes = int64(total - free)

	// Latest CPU/mem sample per server, same 15-minute window the fleet summary
	// uses so the two never disagree, joined to the hourly disk measurement.
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT sv.id, sv.name, sv.status,
		       COALESCE(m.cpu, 0), COALESCE(m.mem_mb, 0),
		       COALESCE(d.size_mb, -1), COALESCE(d.ts, '')
		FROM servers sv
		LEFT JOIN (
			SELECT m.server_id, m.cpu, m.mem_mb FROM metrics m
			JOIN (SELECT server_id, MAX(ts) AS mts FROM metrics
			      WHERE ts >= datetime('now','-15 minutes') GROUP BY server_id) l
			  ON m.server_id = l.server_id AND m.ts = l.mts
		) m ON m.server_id = sv.id
		LEFT JOIN server_disk d ON d.server_id = sv.id
		ORDER BY sv.name COLLATE NOCASE`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out.Servers = []serverUsage{}
	for rows.Next() {
		var u serverUsage
		if rows.Scan(&u.ID, &u.Name, &u.Status, &u.CPU, &u.MemMB, &u.DiskMB, &u.DiskTS) != nil {
			continue
		}
		out.Disk.ServerDataTotal++
		if u.DiskMB >= 0 {
			out.Disk.ServerDataKnown++
			out.Disk.ServerDataBytes += u.DiskMB * mb
			if u.DiskTS > out.Disk.ServerDataSample {
				out.Disk.ServerDataSample = u.DiskTS
			}
		}
		out.Servers = append(out.Servers, u)
	}

	// Three outcomes, not two: the daemon answered, the daemon failed, or there is
	// no daemon wired up at all (tests, and a panel that could not reach Docker at
	// startup). The last one is not an error to report — it is simply a page with
	// one section missing.
	du, duErr := s.dockerDiskUsage(r.Context())
	switch {
	case duErr != nil:
		out.Disk.DockerError = duErr.Error()
	case du != nil:
		out.Disk.Docker = du
		other := out.Disk.UsedBytes - out.Disk.ServerDataBytes -
			du.ImagesBytes - du.ContainersBytes - du.VolumesBytes - du.BuildCacheBytes
		if other < 0 {
			other = 0
		}
		out.Disk.OtherBytes = other
	}

	jsonOK(w, out)
}
