package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// The backup list said when an archive was made, how big it is and whether it
// verified — never WHERE it was written. On a panel with a local disk and a NAS
// that is the first thing you need before a restore, and the thing that decides
// whether this morning's offline mount matters.
//
// The location is resolved server-side on purpose. The browser holds only the
// targets that still exist, so a backup whose target was deleted would render as
// a blank cell — and a blank cell reads as "local", which is the one answer that
// is never safe to guess.

func seedBackupWithTarget(t *testing.T, s *Server, targetName, targetType string) (serverID, targetID, backupID string) {
	t.Helper()
	serverID, targetID, backupID = uuid.New().String(), uuid.New().String(), uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'stopped')`,
		serverID, "backup-loc-test", "minecraft-java", t.TempDir()); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO backup_targets (id, name, type, config_enc) VALUES (?,?,?,'')`,
		targetID, targetName, targetType); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO backups (id, server_id, target_id, path, size_bytes, status)
		 VALUES (?,?,?,'/mnt/nas/ygg/20260928-020000.tar.gz',1234,'done')`,
		backupID, serverID, targetID); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	return serverID, targetID, backupID
}

func listBackups(t *testing.T, s *Server, serverID string) []map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/servers/"+serverID+"/backups", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", serverID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"})
	w := httptest.NewRecorder()
	s.handleListBackups(w, r.WithContext(ctx))

	if w.Code != 200 {
		t.Fatalf("list backups returned %d: %s", w.Code, w.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return out
}

func TestBackupListCarriesItsLocation(t *testing.T) {
	s := testServer(t)
	serverID, _, _ := seedBackupWithTarget(t, s, "Attic NAS", "nfs")

	got := listBackups(t, s, serverID)
	if len(got) != 1 {
		t.Fatalf("got %d backups, want 1", len(got))
	}
	if name, _ := got[0]["target_name"].(string); name != "Attic NAS" {
		t.Errorf("target_name = %q, want %q — the row cannot say where the archive is", name, "Attic NAS")
	}
	if typ, _ := got[0]["target_type"].(string); typ != "nfs" {
		t.Errorf("target_type = %q, want %q", typ, "nfs")
	}
}

// The UI prints "location no longer configured" for a backup whose target name
// is gone, and that has to hold however the target went away. Deleting a target
// does NOT null the reference: db.Open asks for foreign keys in the DSN using
// mattn/go-sqlite3's parameter syntax while the driver is modernc.org/sqlite, so
// PRAGMA foreign_keys reads 0 and every ON DELETE in the schema is inert. Both
// journal_mode=WAL and busy_timeout=5000 are lost the same way. Measured, not
// assumed — the modernc form (_pragma=foreign_keys(1)) returns 1 on the same
// file.
//
// So this asserts what the operator actually sees: the row survives, keeps a
// reference pointing at nothing, and the API reports no name. It is written to
// stay true if the DSN is ever fixed — a nulled target_id produces the same
// empty name and the same sentence on screen.
func TestBackupOutlivesADeletedLocation(t *testing.T) {
	s := testServer(t)
	serverID, targetID, backupID := seedBackupWithTarget(t, s, "Old laptop", "local")

	if _, err := s.db.Exec("DELETE FROM backup_targets WHERE id=?", targetID); err != nil {
		t.Fatalf("delete target: %v", err)
	}

	var stillThere int
	s.db.QueryRow("SELECT COUNT(*) FROM backups WHERE id=?", backupID).Scan(&stillThere)
	if stillThere != 1 {
		t.Fatalf("deleting the target removed the backup row too (count=%d) — "+
			"the archive is still on disk and the panel has forgotten it", stillThere)
	}

	got := listBackups(t, s, serverID)
	if len(got) != 1 {
		t.Fatalf("got %d backups after deleting the target, want 1 — an inner join would "+
			"drop exactly the rows most in need of an explanation", len(got))
	}
	if name, _ := got[0]["target_name"].(string); name != "" {
		t.Errorf("target_name = %q for a location that no longer exists, want \"\" — "+
			"the row would name a target the operator cannot find", name)
	}
	if typ, _ := got[0]["target_type"].(string); typ != "" {
		t.Errorf("target_type = %q for a deleted location, want \"\"", typ)
	}
}
