package api

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

// A scheduled backup that fails must say so in the run log.
//
// It used to return a flat "ok"/"backup started" regardless of outcome, so the
// schedule's run history — the one place you'd look to check that last night's
// backups ran — showed green while the backups had failed. The ❌ notification
// went out, but anyone auditing after the fact saw success.
//
// This drives runAction with a target id that cannot resolve, which is the
// cheapest real failure: runBackup can't load the target config and gives up.
func TestScheduledBackupReportsFailure(t *testing.T) {
	s := testServer(t)
	serverID := uuid.New().String()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'stopped')",
		serverID, "sched-backup-test", "minecraft-java", t.TempDir())

	status, detail := s.runAction(scheduler.ActionBackup, serverID, map[string]string{
		"target_id": "does-not-exist",
	})

	if status != "error" {
		t.Errorf("a failed backup reported status %q (detail %q), want \"error\" — the run log would claim it succeeded",
			status, detail)
	}
	if detail == "" || strings.Contains(detail, "backup started") {
		t.Errorf("detail %q says nothing about the failure", detail)
	}

	// The backups row must agree with the run log.
	var st string
	s.db.QueryRow("SELECT status FROM backups WHERE server_id=?", serverID).Scan(&st)
	if st != "error" {
		t.Errorf("backups row status = %q, want \"error\"", st)
	}
}

// No target configured is a failure too, and always was — guard it so the
// happy-path refactor above can't quietly swallow it.
func TestScheduledBackupWithoutTargetIsAnError(t *testing.T) {
	s := testServer(t)
	status, detail := s.runAction(scheduler.ActionBackup, uuid.New().String(), map[string]string{})
	if status != "error" {
		t.Errorf("missing target reported %q (%q), want \"error\"", status, detail)
	}
}

// runBackup must return an error, not just record one — a caller that gates on
// the backup (a wipe aborts on failure) depends on it.
func TestRunBackupReturnsErrorOnBadTarget(t *testing.T) {
	s := testServer(t)
	serverID := uuid.New().String()
	backupID := uuid.New().String()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'stopped')",
		serverID, "gate-test", "minecraft-java", t.TempDir())
	s.db.Exec("INSERT INTO backups (id, server_id, target_id, status) VALUES (?,?,?,'pending')",
		backupID, serverID, "nope")

	if err := s.runBackup(serverID, "nope", backupID); err == nil {
		t.Fatal("runBackup returned nil for an unresolvable target — a wipe would proceed to delete the world believing it had a backup")
	}
	_ = context.Background()
}
