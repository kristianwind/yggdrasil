package api

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// A pull's whole point is that the caller stops waiting. So the job has to
// carry the outcome the synchronous response used to carry — including the
// import's own report, which is where "ports moved" and "hostnames dropped"
// live. Losing that would make the async version quieter, not just faster.
func TestTransferJobCarriesTheOutcome(t *testing.T) {
	jobs := newTransferJobs()
	jobs.start("j1", "http://kw01:8080")

	got, ok := jobs.get("j1")
	if !ok || got.Status != "running" || got.Source != "http://kw01:8080" {
		t.Fatalf("start: %+v ok=%v", got, ok)
	}

	jobs.update("j1", func(j *transferJob) {
		j.Status = "done"
		j.ServerID = "srv-9"
		j.Name = "3dekoration.dk"
		j.Bytes = 4_400_000_000
		j.Result = map[string]any{"ports_changed": []string{"web 25010→25030"}}
	})
	got, _ = jobs.get("j1")
	if got.Status != "done" || got.ServerID != "srv-9" || got.Result == nil {
		t.Errorf("the finished job must carry the result: %+v", got)
	}

	if _, ok := jobs.get("nope"); ok {
		t.Error("an unknown job id must not report as a job")
	}
}

// get returns a copy. Handing out the pointer would let a reader see a job
// change under it while the goroutine doing the transfer writes to it.
func TestTransferJobGetReturnsACopy(t *testing.T) {
	jobs := newTransferJobs()
	jobs.start("j2", "src")
	snapshot, _ := jobs.get("j2")
	jobs.update("j2", func(j *transferJob) { j.Status = "failed" })
	if snapshot.Status != "running" {
		t.Error("a snapshot taken before the change must not see it — get must copy, not alias")
	}
}

// Finished jobs are forgotten; a running one is never swept, however long it
// has been going. A 4 GB copy over a slow link is not a leak.
func TestSweepKeepsRunningJobs(t *testing.T) {
	jobs := newTransferJobs()
	jobs.start("old-done", "src")
	jobs.start("old-running", "src")
	stale := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	jobs.update("old-done", func(j *transferJob) { j.Status = "done"; j.Started = stale })
	jobs.update("old-running", func(j *transferJob) { j.Started = stale })

	jobs.sweep(6 * time.Hour)

	if _, ok := jobs.get("old-done"); ok {
		t.Error("a long-finished job should have been swept")
	}
	if _, ok := jobs.get("old-running"); !ok {
		t.Error("a still-running job must never be swept, however old — a slow copy is not a leak")
	}
}

// The reader must report progress without holding on to the stream: the import
// consumes it as it arrives, and buffering gigabytes to count them would undo
// the reason pulls stream in the first place.
func TestCountingReaderTicksWithoutBuffering(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 4096))
	var ticks []int64
	// every=0 so each Read reports, making the test deterministic rather than
	// timing-dependent.
	cr := newCountingReader(src, 0, func(n int64) { ticks = append(ticks, n) })

	var sink bytes.Buffer
	n, err := io.Copy(&sink, cr)
	if err != nil || n != 4096 {
		t.Fatalf("copy: n=%d err=%v", n, err)
	}
	if sink.Len() != 4096 {
		t.Errorf("the stream must pass through unchanged, got %d bytes", sink.Len())
	}
	if len(ticks) == 0 {
		t.Fatal("no progress was reported")
	}
	if last := ticks[len(ticks)-1]; last != 4096 {
		t.Errorf("last tick = %d, want the full count", last)
	}
	for i := 1; i < len(ticks); i++ {
		if ticks[i] < ticks[i-1] {
			t.Errorf("progress went backwards: %v", ticks)
			break
		}
	}
}

// A slow link should still say something; a fast one should not publish a
// thousand lines a second. The interval is time-based for that reason.
func TestCountingReaderRespectsTheInterval(t *testing.T) {
	var ticks int
	cr := newCountingReader(strings.NewReader(strings.Repeat("x", 100000)), time.Hour, func(int64) { ticks++ })
	io.Copy(io.Discard, cr) //nolint:errcheck
	if ticks != 0 {
		t.Errorf("got %d ticks with an hour-long interval, want none", ticks)
	}
	if cr.n != 100000 {
		t.Errorf("the count must still be accurate without ticking: %d", cr.n)
	}
}
