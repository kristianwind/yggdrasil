package api

import (
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// A pull used to be one synchronous HTTP request: the browser asked, and the
// panel answered nothing until gigabytes had crossed the network and been
// unpacked. Anything between the two ends with a timeout eventually killed it —
// measured as a Cloudflare 524 on a 4.1 GB site, where the transfer itself was
// running perfectly and only the browser's request died.
//
// Telling people "don't reach your panel through your own domain" is a
// workaround, not a design. So the request now returns immediately with a job,
// and progress streams over the same WebSocket machinery the install log has
// always used.
//
// The side benefit is the one that was actually missing: you can see how far a
// multi-gigabyte copy has got, instead of watching a browser tab.

type transferJob struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // running | done | failed
	Source   string `json:"source"`
	ServerID string `json:"server_id,omitempty"` // set on success
	Name     string `json:"name,omitempty"`
	Error    string `json:"error,omitempty"`
	Started  string `json:"started"`
	Bytes    int64  `json:"bytes"`
	// Result carries the import's own report — ports moved, hostnames dropped —
	// so the UI can say the same things it said when this was synchronous.
	Result map[string]any `json:"result,omitempty"`
}

// transferJobTTL is how long a finished job stays readable. Long enough that a
// browser left on another tab still finds out how its transfer went.
const transferJobTTL = 6 * time.Hour

type transferJobs struct {
	mu   sync.Mutex
	jobs map[string]*transferJob
}

func newTransferJobs() *transferJobs { return &transferJobs{jobs: map[string]*transferJob{}} }

func (t *transferJobs) start(id, source string) *transferJob {
	// Swept here rather than on a ticker: the only thing that grows this map is
	// starting a job, so that is the only moment it can need trimming. One less
	// goroutine for a map that holds a handful of entries.
	t.sweep(transferJobTTL)
	t.mu.Lock()
	defer t.mu.Unlock()
	j := &transferJob{ID: id, Status: "running", Source: source, Started: time.Now().UTC().Format(time.RFC3339)}
	t.jobs[id] = j
	return j
}

func (t *transferJobs) get(id string) (transferJob, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	if !ok {
		return transferJob{}, false
	}
	return *j, true // a copy: callers must not race the writer
}

func (t *transferJobs) update(id string, f func(*transferJob)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[id]; ok {
		f(j)
	}
}

// sweep drops finished jobs after a while. In memory on purpose — a job is a
// thing you are watching, not a record; the import writes the durable outcome
// (the server row, the audit entry) itself.
func (t *transferJobs) sweep(maxAge time.Duration) {
	cutoff := time.Now().UTC().Add(-maxAge)
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, j := range t.jobs {
		if j.Status == "running" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, j.Started); err == nil && ts.Before(cutoff) {
			delete(t.jobs, id)
		}
	}
}

// countingReader reports progress as the bundle streams past, without buffering
// any of it. The import consumes the stream as it arrives; this only watches.
type countingReader struct {
	r        io.Reader
	n        int64
	onTick   func(int64)
	lastTick time.Time
	every    time.Duration
}

func newCountingReader(r io.Reader, every time.Duration, onTick func(int64)) *countingReader {
	return &countingReader{r: r, onTick: onTick, every: every, lastTick: time.Now()}
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	// Time-based, not byte-based: a slow link should still say something, and a
	// fast one should not publish a thousand lines a second.
	if c.onTick != nil && time.Since(c.lastTick) >= c.every {
		c.lastTick = time.Now()
		c.onTick(c.n)
	}
	return n, err
}

// handleTransferLog streams a pull's progress, reusing the install hub — the
// same pub/sub, the same bounded history, so a browser that connects late or
// reconnects still sees what it missed. Keyed by job id rather than server id,
// because a pull has no server until it finishes.
func (s *Server) handleTransferLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	kaStop := make(chan struct{})
	defer close(kaStop)
	go wsKeepalive(conn, kaStop)

	ch, history := s.install.subscribe(id)
	defer s.install.unsubscribe(id, ch)

	for _, line := range history {
		if conn.WriteMessage(websocket.TextMessage, []byte(line)) != nil {
			return
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case line := <-ch:
			if conn.WriteMessage(websocket.TextMessage, []byte(line)) != nil {
				return
			}
		case <-done:
			return
		}
	}
}
