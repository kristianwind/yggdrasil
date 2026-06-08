package api

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/query"
	"github.com/kristianwind/yggdrasil/internal/rcon"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
	"github.com/robfig/cron/v3"
)

// schedulerState holds the running cron and is rebuilt whenever schedules change.
type schedulerState struct {
	mu   sync.Mutex
	cron *cron.Cron
}

// StartScheduler seeds default message templates and starts the cron runner.
func (s *Server) StartScheduler() {
	s.seedTemplates()
	s.sched = &schedulerState{}
	s.reloadSchedules()
}

func (s *Server) seedTemplates() {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM message_templates").Scan(&count)
	if count > 0 {
		return
	}
	for _, t := range scheduler.DefaultTemplates {
		s.db.Exec("INSERT INTO message_templates (id, name, body, builtin) VALUES (?,?,?,1)",
			uuid.New().String(), t.Name, t.Body)
	}
}

// reloadSchedules rebuilds the cron from the enabled schedules in the DB.
func (s *Server) reloadSchedules() {
	if s.sched == nil {
		return
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()

	if s.sched.cron != nil {
		s.sched.cron.Stop()
	}
	// 6-field cron (with seconds) is accepted; 5-field too via the parser.
	c := cron.New(cron.WithParser(cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
	)))

	rows, err := s.db.Query("SELECT id, cron_expr FROM schedules WHERE enabled=1")
	if err != nil {
		s.sched.cron = c
		c.Start()
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, expr string
		if rows.Scan(&id, &expr) != nil {
			continue
		}
		sid := id
		if _, err := c.AddFunc(expr, func() { s.runScheduleByID(sid) }); err != nil {
			log.Printf("scheduler: bad cron %q for schedule %s: %v", expr, sid, err)
		}
	}
	s.sched.cron = c
	c.Start()
}

// runScheduleByID loads a schedule and executes its action over its scope,
// recording one schedule_runs row per target server so the UI can show a log.
func (s *Server) runScheduleByID(id string) {
	defer recoverLog("runScheduleByID")
	var name, action, argsJSON, serverID, realmID string
	err := s.db.QueryRow(
		"SELECT name, action, COALESCE(args_json,'{}'), COALESCE(server_id,''), COALESCE(realm_id,'') FROM schedules WHERE id=?",
		id).Scan(&name, &action, &argsJSON, &serverID, &realmID)
	if err != nil {
		return
	}
	var args map[string]string
	json.Unmarshal([]byte(argsJSON), &args)
	if args == nil {
		args = map[string]string{}
	}
	targets := s.scopeServers(serverID, realmID)
	if len(targets) == 0 {
		s.recordRun(id, name, "", "", action, "skipped", "no servers in scope")
		return
	}
	for _, srv := range targets {
		status, detail := s.runAction(scheduler.Action(action), srv, args)
		s.recordRun(id, name, srv, s.serverName(srv), action, status, detail)
	}
}

// recordRun appends a run-log entry and prunes to the last 100 per schedule.
func (s *Server) recordRun(scheduleID, scheduleName, serverID, serverName, action, status, detail string) {
	s.db.Exec(
		"INSERT INTO schedule_runs (id, schedule_id, schedule_name, server_id, server_name, action, status, detail) VALUES (?,?,?,?,?,?,?,?)",
		uuid.New().String(), scheduleID, scheduleName, serverID, serverName, action, status, detail)
	s.db.Exec(`DELETE FROM schedule_runs WHERE schedule_id=? AND id NOT IN (
		SELECT id FROM schedule_runs WHERE schedule_id=? ORDER BY ran_at DESC, rowid DESC LIMIT 100)`,
		scheduleID, scheduleID)
}

// scopeServers resolves the target servers for a schedule: a single server, all
// servers in a realm, or all servers (global).
func (s *Server) scopeServers(serverID, realmID string) []string {
	if serverID != "" {
		return []string{serverID}
	}
	var q string
	var arg any
	if realmID != "" {
		q, arg = "SELECT id FROM servers WHERE realm_id=?", realmID
	} else {
		q = "SELECT id FROM servers"
	}
	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
	}
	var err error
	if arg != nil {
		rows, err = s.db.Query(q, arg)
	} else {
		rows, err = s.db.Query(q)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var sid string
		if rows.Scan(&sid) == nil {
			ids = append(ids, sid)
		}
	}
	return ids
}

// runAction executes one scheduled action against one server and returns a
// (status, detail) pair for the run log: status is "ok", "skipped" or "error".
func (s *Server) runAction(action scheduler.Action, serverID string, args map[string]string) (status, detail string) {
	ctx := context.Background()
	switch action {
	case scheduler.ActionBackup:
		target := args["target_id"]
		if target == "" {
			return "error", "no backup target configured"
		}
		backupID := uuid.New().String()
		s.db.Exec("INSERT INTO backups (id, server_id, target_id, status) VALUES (?,?,?,'pending')",
			backupID, serverID, target)
		s.runBackup(serverID, target, backupID)
		return "ok", "backup started"

	case scheduler.ActionRestart:
		if args["skip_if_players"] == "true" && s.playersOnline(serverID) > 0 {
			return "skipped", "players online"
		}
		// Recreate the container (not a plain docker-restart) so a scheduled restart
		// applies any rune/env/mod changes too, consistent with the manual restart.
		cid := s.containerID(serverID)
		if cid == "" {
			return "error", "no container (server not started)"
		}
		if err := s.recreateAndStart(ctx, serverID); err != nil {
			s.docker.Restart(ctx, cid) // fall back to a plain restart on error
			return "ok", "restarted (fallback after: " + err.Error() + ")"
		}
		return "ok", "restarted"

	case scheduler.ActionStart:
		cid := s.containerID(serverID)
		if cid == "" {
			return "error", "no container to start"
		}
		if err := s.docker.Start(ctx, cid); err != nil {
			return "error", err.Error()
		}
		s.db.Exec("UPDATE servers SET status='running' WHERE id=?", serverID)
		return "ok", "started"

	case scheduler.ActionStop:
		cid := s.containerID(serverID)
		if cid == "" {
			return "skipped", "already stopped (no container)"
		}
		if err := s.docker.Stop(ctx, cid, 30); err != nil {
			return "error", err.Error()
		}
		s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", serverID)
		return "ok", "stopped"

	case scheduler.ActionCommand:
		cmd := args["command"]
		if cmd == "" {
			return "skipped", "no command configured"
		}
		s.sendToServer(serverID, cmd)
		return "ok", "sent: " + cmd

	case scheduler.ActionMessage:
		body := args["text"]
		if tid := args["template_id"]; tid != "" {
			s.db.QueryRow("SELECT body FROM message_templates WHERE id=?", tid).Scan(&body)
		}
		if body == "" {
			return "skipped", "no message body"
		}
		vars := map[string]string{
			"server_name": s.serverName(serverID),
			"minutes":     args["minutes"],
			"seconds":     args["seconds"],
		}
		rendered := scheduler.Render(body, vars)
		s.sendToServer(serverID, rendered)
		return "ok", "message sent: " + rendered

	case scheduler.ActionUpdate:
		if args["skip_if_players"] == "true" && s.playersOnline(serverID) > 0 {
			return "skipped", "players online"
		}
		s.runInstall(serverID) //nolint:errcheck
		return "ok", "update/reinstall started"
	}
	return "error", "unknown action: " + string(action)
}

// sendToServer delivers a command to a server via RCON, falling back to the
// container's stdin (console) for games without RCON.
func (s *Server) sendToServer(serverID, command string) {
	rt, err := s.loadRuntime(context.Background(), serverID)
	if err != nil {
		return
	}
	if rt.gs.RCON != nil && rt.gs.RCON.Enabled {
		port := rt.ports["rcon"]
		if port == 0 {
			port = rt.ports["game"]
		}
		pw := ""
		if rt.gs.RCON.PasswordVar != "" {
			pw = rt.env[rt.gs.RCON.PasswordVar]
		}
		client, err := rcon.Dial(rcon.Config{
			Type: rt.gs.RCON.Type, Host: "127.0.0.1", Port: port, Password: pw, Timeout: 5 * time.Second,
		})
		if err == nil {
			defer client.Close()
			client.Execute(command)
			return
		}
	}
	// Fallback: write to container stdin.
	if cid := s.containerID(serverID); cid != "" {
		s.docker.SendStdin(context.Background(), cid, command)
	}
}

// playersOnline queries a server's player count; returns -1 if unknown.
func (s *Server) playersOnline(serverID string) int {
	rt, err := s.loadRuntime(context.Background(), serverID)
	if err != nil || rt.gs.Query == nil {
		return -1
	}
	st, err := query.Query(rt.gs.Query.Type, "127.0.0.1", rt.queryPort(), 3*time.Second)
	if err != nil {
		return -1
	}
	return st.Players
}

func (s *Server) containerID(serverID string) string {
	var cid string
	s.db.QueryRow("SELECT COALESCE(container_id,'') FROM servers WHERE id=?", serverID).Scan(&cid)
	return cid
}

func (s *Server) serverName(serverID string) string {
	var name string
	s.db.QueryRow("SELECT name FROM servers WHERE id=?", serverID).Scan(&name)
	return name
}
