package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
)

// seedStatsFixture builds a fleet with one of each interesting case:
//
//	big     — measured disk, a fresh CPU/mem sample
//	small   — measured disk, no sample at all (stopped)
//	stale   — measured disk, a sample too old to count
//	unknown — never measured
func seedStatsFixture(t *testing.T, s *Server) {
	t.Helper()
	s.db.Exec("INSERT INTO gameskills (id, name, yaml) VALUES ('gs','gs','')")
	for _, sv := range []struct{ id, name, status string }{
		{"big", "Big Server", "running"},
		{"small", "Small Server", "stopped"},
		{"stale", "Stale Server", "running"},
		{"unknown", "Unmeasured Server", "running"},
	} {
		if _, err := s.db.Exec(
			"INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES (?,?,'gs',?,?)",
			sv.id, sv.name, sv.status, "/tmp/"+sv.id); err != nil {
			t.Fatalf("seed server %s: %v", sv.id, err)
		}
	}

	s.db.Exec("INSERT INTO server_disk (server_id, size_mb, ts) VALUES ('big', 5000, datetime('now'))")
	s.db.Exec("INSERT INTO server_disk (server_id, size_mb, ts) VALUES ('small', 10, datetime('now'))")
	s.db.Exec("INSERT INTO server_disk (server_id, size_mb, ts) VALUES ('stale', 100, datetime('now'))")

	s.db.Exec("INSERT INTO metrics (server_id, ts, cpu, mem_mb, players) VALUES ('big', datetime('now','-1 minutes'), 42.5, 2048, 0)")
	// Older row for the same server — the query must take the newest, not both.
	s.db.Exec("INSERT INTO metrics (server_id, ts, cpu, mem_mb, players) VALUES ('big', datetime('now','-10 minutes'), 5, 100, 0)")
	// Outside the 15-minute window: must not be picked up at all.
	s.db.Exec("INSERT INTO metrics (server_id, ts, cpu, mem_mb, players) VALUES ('stale', datetime('now','-3 hours'), 99, 9999, 0)")
}

func statsFor(t *testing.T, s *Server) statsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleSystemStats(rec, httptest.NewRequest(http.MethodGet, "/api/system/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	return got
}

func TestSystemStatsRanksAndSeparatesUnmeasuredFromEmpty(t *testing.T) {
	s := testServer(t)
	s.cfg = &config.Config{}
	s.cfg.Database.Path = t.TempDir() + "/x.db"
	seedStatsFixture(t, s)

	got := statsFor(t, s)

	if len(got.Servers) != 4 {
		t.Fatalf("got %d servers, want 4", len(got.Servers))
	}
	by := map[string]serverUsage{}
	for _, u := range got.Servers {
		by[u.ID] = u
	}

	// The distinction the whole page rests on: a server nobody has measured must
	// not read as a server using no disk. Zero is an answer; -1 is the absence of
	// one, and the UI prints "not measured yet" for it.
	if by["unknown"].DiskMB != -1 {
		t.Errorf("unmeasured server DiskMB = %d, want -1", by["unknown"].DiskMB)
	}
	if by["small"].DiskMB != 10 {
		t.Errorf("small DiskMB = %d, want 10", by["small"].DiskMB)
	}

	// Newest sample within the window wins; anything older is not a current
	// reading and must not be summed in beside it.
	if by["big"].CPU != 42.5 {
		t.Errorf("big CPU = %v, want 42.5 (the 1-minute-old row, not the 10-minute-old one)", by["big"].CPU)
	}
	if by["big"].MemMB != 2048 {
		t.Errorf("big MemMB = %v, want 2048", by["big"].MemMB)
	}
	if by["stale"].CPU != 0 {
		t.Errorf("stale CPU = %v, want 0 — a 3-hour-old sample is not a current reading", by["stale"].CPU)
	}

	// Only measured directories count toward the total, and the page is told how
	// many that was so it can say so rather than implying the sum is complete.
	if got.Disk.ServerDataTotal != 4 {
		t.Errorf("ServerDataTotal = %d, want 4", got.Disk.ServerDataTotal)
	}
	if got.Disk.ServerDataKnown != 3 {
		t.Errorf("ServerDataKnown = %d, want 3", got.Disk.ServerDataKnown)
	}
	const mb = 1024 * 1024
	if want := int64(5110) * mb; got.Disk.ServerDataBytes != want {
		t.Errorf("ServerDataBytes = %d, want %d", got.Disk.ServerDataBytes, want)
	}
}

// A panel with no servers must render, not divide by zero or emit a null list
// that the page would try to sort.
func TestSystemStatsEmptyFleet(t *testing.T) {
	s := testServer(t)
	s.cfg = &config.Config{}
	s.cfg.Database.Path = t.TempDir() + "/x.db"

	got := statsFor(t, s)
	if got.Servers == nil {
		t.Error("Servers is null; want an empty array so the page can sort it")
	}
	if len(got.Servers) != 0 {
		t.Errorf("got %d servers, want 0", len(got.Servers))
	}
	if got.Disk.DockerError != "" {
		t.Errorf("DockerError = %q, want empty — no daemon wired up is not an error to report", got.Disk.DockerError)
	}
}
