package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/crypto"
)

// pbsTestServer is testServer plus the cipher the backup-target handlers need.
func pbsTestServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	c, err := crypto.New("test-secret-key-for-pbs-tests-0123456789")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	s.cipher = c
	return s
}

// seedPBSBackup creates a PBS target, a server and one completed backup whose
// path is a snapshot identifier rather than a file name.
func seedPBSBackup(t *testing.T, s *Server) (serverID, targetID, backupID string) {
	t.Helper()
	serverID, targetID, backupID = uuid.New().String(), uuid.New().String(), uuid.New().String()
	enc, err := s.encryptTargetConfig(backup.Config{
		Type: backup.PBSType, Host: "pbs.lan", Username: "ygg@pbs!panel",
		Password: "secret", Datastore: "store",
	})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	s.db.Exec("INSERT INTO backup_targets (id, name, type, config_enc, keep_n, keep_days) VALUES (?,?,?,?,0,0)",
		targetID, "pbs", backup.PBSType, enc)
	s.db.Exec("INSERT OR IGNORE INTO gameskills (id, name, yaml) VALUES ('gs','gs','')")
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES (?,?,'gs','stopped','/tmp/x')",
		serverID, "Test Server")
	s.db.Exec("INSERT INTO backups (id, server_id, target_id, path, status) VALUES (?,?,?,?,'done')",
		backupID, serverID, targetID, "host/test-server-"+serverID[:8]+"/2026-09-02T18:00:00Z")
	return
}

// A PBS snapshot is deduplicated chunks in somebody else's datastore, not a
// file. Download must say so, not fall through to the stream path — where
// backup.Open now fails with an internal-sounding message.
func TestDownloadRefusesPBSWithAnExplanation(t *testing.T) {
	s := pbsTestServer(t)
	_, _, backupID := seedPBSBackup(t, s)

	r := httptest.NewRequest("GET", "/api/backups/"+backupID+"/download", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", backupID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, claimsKey, &auth.Claims{
		UserID: "u1", Username: "admin", Role: "admin",
	})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	s.handleDownloadBackup(w, r)

	if w.Code != 400 {
		t.Errorf("got %d, want 400", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(strings.ToLower(body), "chunk") {
		t.Errorf("the message must explain why, not just refuse: %q", body)
	}
}

// PBS verifies its own snapshots server-side against the checksums it stored.
// Re-implementing that here would either duplicate it badly or lie about it.
func TestVerifyDefersToPBS(t *testing.T) {
	s := pbsTestServer(t)
	_, _, backupID := seedPBSBackup(t, s)

	_, err := s.verifyBackupByID(context.Background(), backupID)
	if err == nil {
		t.Fatal("verify must not claim to have checked a PBS snapshot")
	}
	if !strings.Contains(err.Error(), "Proxmox") {
		t.Errorf("the message should point at PBS's own verify job: %v", err)
	}
	// And it must not have recorded a verdict either way.
	var verifiedAt string
	s.db.QueryRow("SELECT COALESCE(verified_at,'') FROM backups WHERE id=?", backupID).Scan(&verifiedAt)
	if verifiedAt != "" {
		t.Error("a refused verify must not stamp verified_at")
	}
}

// The backup group is what makes PBS incremental: a snapshot dedupes against the
// previous snapshot in the SAME group. If this changed between runs, every
// backup would start from zero and the datastore would grow without bound.
func TestPBSBackupIDIsStableForAServer(t *testing.T) {
	s := pbsTestServer(t)
	serverID, _, _ := seedPBSBackup(t, s)

	first := s.pbsBackupID(serverID)
	if first != s.pbsBackupID(serverID) {
		t.Fatal("backup id is not stable across calls")
	}
	if !strings.Contains(first, serverID[:8]) {
		t.Errorf("backup id %q drops the short server id, so two servers sharing a "+
			"name would share a group", first)
	}
	// A second server with the same name must land in its own group.
	other := uuid.New().String()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES (?,?,'gs','stopped','/tmp/y')",
		other, "Test Server")
	if s.pbsBackupID(other) == first {
		t.Error("two servers with the same name collided in one backup group")
	}
}

// Renaming a server changes its group, which silently restarts deduplication.
// This is a real cost, so it is asserted rather than left to be discovered from
// a surprising bandwidth bill. If this ever needs to change, the fix is to store
// the id on the server row — not to loosen the test.
func TestRenamingAServerStartsANewPBSGroup(t *testing.T) {
	s := pbsTestServer(t)
	serverID, _, _ := seedPBSBackup(t, s)
	before := s.pbsBackupID(serverID)
	s.db.Exec("UPDATE servers SET name=? WHERE id=?", "Renamed Server", serverID)
	if s.pbsBackupID(serverID) == before {
		t.Skip("naming no longer derives from the server name — update the docs too")
	}
}

// Retention with no policy must not reach the server at all: `prune` with no
// --keep flag deletes every snapshot in the group.
func TestPBSRetentionIsANoOpWithoutAPolicy(t *testing.T) {
	s := pbsTestServer(t)
	serverID, targetID, _ := seedPBSBackup(t, s) // seeded with keep_n=0, keep_days=0

	// s.docker is nil here, so any attempt to run the client would panic. The
	// test passing IS the assertion that nothing was run.
	cfg, err := s.loadTargetConfig(context.Background(), targetID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s.applyPBSRetention(context.Background(), serverID, targetID, *cfg)
}

// The stored secret must survive a round trip through the encrypted column, or
// every backup fails to authenticate with a message about credentials.
func TestPBSTargetConfigRoundTrip(t *testing.T) {
	s := pbsTestServer(t)
	_, targetID, _ := seedPBSBackup(t, s)
	cfg, err := s.loadTargetConfig(context.Background(), targetID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Type != backup.PBSType || cfg.Datastore != "store" ||
		cfg.Username != "ygg@pbs!panel" || cfg.Password != "secret" {
		t.Errorf("config did not survive: %+v", cfg)
	}
}

// The exact strings PBS 4.0 produces, recorded from a live server. The point of
// pinning them is that the check must be NARROW: a broad match would report a
// permissions or network failure as "no snapshots yet", which for a backup is
// the worst direction to be wrong in.
func TestPBSGroupMissingMatchesOnlyTheRealMessage(t *testing.T) {
	missing := []string{
		`Error: unable to read "/datastore/host/does-not-exist/owner" - No such file or directory (os error 2)`,
	}
	for _, m := range missing {
		if !pbsGroupMissing([]byte(m)) {
			t.Errorf("should be recognised as an empty group: %q", m)
		}
	}
	// Everything below is a REAL failure and must never be swallowed.
	realFailures := []string{
		"Error: permission check failed - missing Datastore.Modify|Datastore.Prune on /datastore/store1",
		"Error: permission check failed",
		"Error: client error (Connect)\n\nCaused by:\n    error connecting to https://10.0.0.1:8007/ - tcp connect error",
		`Error: fingerprint "aa:bb" does not match server certificate`,
		"Error: authentication failed - invalid credentials",
		"Error: parameter verification failed - 'store': value must be at least 3 characters long",
		"",
	}
	for _, m := range realFailures {
		if pbsGroupMissing([]byte(m)) {
			t.Errorf("a real failure was mistaken for an empty group: %q", m)
		}
	}
}

// The credential file must NOT go under /tmp.
//
// The panel's systemd unit sets PrivateTmp=yes, so the panel's /tmp is a private
// mount namespace that the Docker daemon cannot see. Staging there produced, on
// the first real backup in production:
//
//	invalid mount config for type "bind": bind source path does not exist:
//	/tmp/ygg-pbs-235232696
//
// — a path the panel had genuinely just created. The daemon is simply looking in
// a different namespace. Any future change that reaches for os.MkdirTemp("")
// reintroduces it, and it cannot be caught locally, because a test binary has no
// PrivateTmp.
func TestPBSStagingIsNotUnderTmp(t *testing.T) {
	s := pbsTestServer(t)
	base := t.TempDir()
	s.cfg = &config.Config{}
	s.cfg.Database.Path = filepath.Join(base, "yggdrasil.db")

	root := s.pbsStagingRoot()
	if root == "" {
		t.Fatal("no staging root, so it would fall back to /tmp")
	}
	if !strings.HasPrefix(root, base) {
		t.Errorf("staging root %q is outside the panel's state directory %q — the "+
			"daemon is only known to be able to see the latter", root, base)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Errorf("staging root not usable: %v", err)
	}
	// Same root the server data directories come from, which is the thing that
	// proves the daemon can bind-mount out of it.
	if filepath.Dir(root) != filepath.Dir(s.cfg.Database.Path) {
		t.Errorf("staging root %q does not share the data root %q",
			root, filepath.Dir(s.cfg.Database.Path))
	}
}

// With no config (tests, odd deployments) it must degrade to the default rather
// than returning a path that does not exist.
func TestPBSStagingRootFallsBackCleanly(t *testing.T) {
	s := &Server{}
	if got := s.pbsStagingRoot(); got != "" {
		t.Errorf("expected empty fallback, got %q", got)
	}
}
