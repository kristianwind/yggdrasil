package api

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// App events: the general form of the player-session recorder. A rune's events:
// block declares notable log lines (WordPress xmlrpc/login attempts, nginx 5xx,
// failed auth…); each metrics tick we read the log since we last looked and roll
// matches up per subject (capture group 1, e.g. a client IP) per hour into
// app_events. That turns a brute-force burst into one row/IP/hour instead of
// thousands of raw lines — a durable, low-volume security/health signal Kvasir
// (and the UI) can query, without storing raw access logs.

var appEventCursor sync.Map // serverID -> RFC3339 timestamp string

// maxEventBucketsPerTick bounds how many distinct (key,subject,hour) rows one
// tick may write, so a widely-distributed attack can't balloon the DB in a burst.
const maxEventBucketsPerTick = 2000

// sampleAppEvents records declared log events for every running server whose rune
// has an events: block. Called from the metrics sampler tick.
func (s *Server) sampleAppEvents() {
	defer recoverLog("sampleAppEvents")
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status='running' AND container_id<>''")
	if err != nil {
		return
	}
	type sv struct{ id, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		rt, err := s.loadRuntime(context.Background(), x.id)
		if err != nil || len(rt.gs.Events) == 0 {
			continue
		}
		s.recordAppEvents(x.id, x.cid, rt.gs.Events)
	}
}

type eventAgg struct {
	label  string
	count  int
	lastTS string // SQLite datetime
}

// recordAppEvents reads the log since the cursor and upserts aggregated matches.
func (s *Server) recordAppEvents(serverID, cid string, events []gameskill.Event) {
	type compiled struct {
		key, label string
		re         *regexp.Regexp
	}
	var defs []compiled
	for _, ev := range events {
		re, err := regexp.Compile(ev.Match)
		if err != nil {
			continue
		}
		defs = append(defs, compiled{key: ev.Key, label: firstNonEmpty(ev.Label, ev.Key), re: re})
	}
	if len(defs) == 0 {
		return
	}

	since := "10m"
	if v, ok := appEventCursor.Load(serverID); ok {
		since = v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rc, err := s.docker.LogsExport(ctx, cid, docker.LogExportOptions{Since: since, Timestamps: true, Tail: "all"})
	if err != nil {
		return
	}
	defer rc.Close()
	var buf bytes.Buffer
	docker.DemuxCopy(&buf, io.LimitReader(rc, 16<<20)) //nolint:errcheck

	agg := map[string]*eventAgg{} // key \x00 subject \x00 bucket -> agg
	newest := ""
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sp := strings.SplitN(line, " ", 2)
		if len(sp) != 2 {
			continue
		}
		ts, msg := sp[0], sp[1]
		et, terr := time.Parse(time.RFC3339Nano, ts)
		if terr != nil {
			continue
		}
		if ts > newest {
			newest = ts
		}
		bucket := et.UTC().Truncate(time.Hour).Format("2006-01-02 15:04:05")
		sqlTS := et.UTC().Format("2006-01-02 15:04:05")
		for _, d := range defs {
			m := d.re.FindStringSubmatch(msg)
			if m == nil {
				continue
			}
			subject := ""
			if len(m) > 1 {
				subject = strings.TrimSpace(m[1])
				if len(subject) > 64 {
					subject = subject[:64]
				}
			}
			k := d.key + "\x00" + subject + "\x00" + bucket
			a := agg[k]
			if a == nil {
				if len(agg) >= maxEventBucketsPerTick {
					continue // safety valve — drop the long tail this tick
				}
				a = &eventAgg{label: d.label}
				agg[k] = a
			}
			a.count++
			if sqlTS > a.lastTS {
				a.lastTS = sqlTS
			}
		}
	}

	for k, a := range agg {
		parts := strings.SplitN(k, "\x00", 3)
		s.db.Exec(`
			INSERT INTO app_events (server_id, key, label, subject, bucket, count, last_ts)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(server_id, key, subject, bucket) DO UPDATE SET
				count = count + excluded.count,
				last_ts = CASE WHEN excluded.last_ts > last_ts THEN excluded.last_ts ELSE last_ts END,
				label = excluded.label`,
			serverID, parts[0], a.label, parts[1], parts[2], a.count, a.lastTS)
	}
	if newest != "" {
		appEventCursor.Store(serverID, newest)
	}
}
