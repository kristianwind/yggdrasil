package api

import (
	"fmt"
	"time"
)

// A crash tells you a server died. Nothing tells you it is still dead.
//
// Heimdal, a DayZ server on the game box, exited at 02:04 on 11 August. The
// panel caught it: the exit went into server_crashes, a notification went out,
// and Kvasir looked at it. Then it stayed down for two days. The nightly
// schedules kept running and kept writing "update: skipped — server is
// stopped", so the panel knew, repeatedly, and never said so again.
//
// One message at two in the morning is not a report that a server is down; it
// is a report that a server went down, and those are different. This closes
// that: while a server remains stopped after an unexpected exit, it is raised
// again on a slow cadence until someone deals with it.
const (
	// How long a server may stay down after a crash before it is worth saying
	// something. Long enough that a restart, an image pull or a host reboot
	// finishes without a word.
	stillDownGrace = 2 * time.Hour
	// And how often to repeat it afterwards. Daily, not hourly: the useful
	// reminder about a server dead since 02:00 is one a day. Hourly is how a
	// real outage turns into something people filter out.
	stillDownRepeat = 24 * time.Hour
	stillDownScan   = 15 * time.Minute
)

func (s *Server) startStillDownLoop() {
	go func() {
		defer recoverLog("stillDownLoop")
		t := time.NewTicker(stillDownScan)
		defer t.Stop()
		for range t.C {
			s.checkStillDown()
		}
	}()
}

// checkStillDown raises an incident for every installed server that crashed,
// is still stopped, and has not been touched by anyone since.
func (s *Server) checkStillDown() {
	defer recoverLog("checkStillDown")

	// The join is the whole point. A server sitting at status='stopped' is
	// usually stopped on purpose, and paging about those would make this
	// useless within a day — so it only counts when the most recent thing that
	// happened to the server was an unexpected exit, with no operator action
	// after it. Someone who stops a server has said what they want; someone
	// whose server exited on its own has not.
	rows, err := s.db.Query(`
		SELECT sv.id, sv.name, c.exit_code, datetime(c.ts),
		       CAST((julianday('now') - julianday(c.ts)) * 24 AS INT)
		  FROM servers sv
		  JOIN (SELECT server_id, MAX(ts) AS ts, exit_code
		          FROM server_crashes GROUP BY server_id) c ON c.server_id = sv.id
		 WHERE sv.installed = 1
		   AND sv.status = 'stopped'
		   AND datetime(c.ts) <= datetime('now', ?)
		   AND NOT EXISTS (
		         SELECT 1 FROM audit_log a
		          WHERE a.resource = 'server:' || sv.id
		            AND datetime(a.ts) > datetime(c.ts))`,
		fmt.Sprintf("-%d minutes", int(stillDownGrace.Minutes())))
	if err != nil {
		return
	}
	type down struct {
		id, name, since string
		exitCode, hours int
	}
	var list []down
	for rows.Next() {
		var d down
		if rows.Scan(&d.id, &d.name, &d.exitCode, &d.since, &d.hours) == nil {
			list = append(list, d)
		}
	}
	rows.Close()

	for _, d := range list {
		title := fmt.Sprintf("%s has been down for %dh", d.name, d.hours)
		detail := fmt.Sprintf("Exited with code %d at %s and has not been started since.",
			d.exitCode, d.since)
		if s.raiseIncident(d.id, "still-down", title, detail, stillDownRepeat) {
			go s.notifyServer(d.id, "🔴 "+title+"\n"+detail)
		}
	}
}
