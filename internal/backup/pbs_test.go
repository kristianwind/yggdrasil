package backup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPBSRepository(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"api token", Config{Username: "ygg@pbs!panel", Host: "pbs.lan", Datastore: "store1"},
			"ygg@pbs!panel@pbs.lan:store1"},
		{"default port is omitted", Config{Username: "root@pam", Host: "pbs.lan", Port: 8007, Datastore: "s"},
			"root@pam@pbs.lan:s"},
		{"non-default port", Config{Username: "root@pam", Host: "pbs.lan", Port: 1234, Datastore: "s"},
			"root@pam@pbs.lan:1234:s"},
		{"no auth id", Config{Host: "pbs.lan", Datastore: "s"}, "pbs.lan:s"},
		// Unbracketed, the colons in an IPv6 literal are read as the port
		// separator and the repository silently points somewhere else.
		{"ipv6 gets brackets", Config{Username: "u@pbs", Host: "fd00::5", Datastore: "s"},
			"u@pbs@[fd00::5]:s"},
		{"ipv6 with port", Config{Username: "u@pbs", Host: "fd00::5", Port: 1234, Datastore: "s"},
			"u@pbs@[fd00::5]:1234:s"},
		{"ipv4 is untouched", Config{Host: "192.168.1.5", Datastore: "s"}, "192.168.1.5:s"},
	} {
		if got := PBSRepository(tc.cfg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The "!" in an API token id is a shell metacharacter. It must survive intact,
// which is only true because it is never interpolated into a command string.
func TestPBSRepositoryKeepsTokenSeparator(t *testing.T) {
	r := PBSRepository(Config{Username: "ygg@pbs!panel", Host: "h", Datastore: "d"})
	if !strings.Contains(r, "!panel@") {
		t.Errorf("token id mangled: %q", r)
	}
}

func TestPBSBackupIDIsAcceptedByTheServer(t *testing.T) {
	if got := PBSBackupID("minecraft-survival", "a1b2c3d4"); got != "minecraft-survival-a1b2c3d4" {
		t.Errorf("normal name: got %q", got)
	}
	// Whatever the name, the result has to satisfy the pattern PBS enforces
	// server-side — a violation is rejected by the API with a message about the
	// id, which reads like a permissions problem. The short id must survive so
	// two servers sharing a name still get separate groups.
	for _, tc := range []struct{ slug, short string }{
		{"minecraft-survival", "a1b2c3d4"},
		{"", "a1b2c3d4"},
		{"-weird", "abc12345"},
		{"...", "abc12345"},
		{"Ærlig Dansk Navn", "abc12345"},
		{"////", "abc12345"},
	} {
		got := PBSBackupID(tc.slug, tc.short)
		if !pbsIDSafe.MatchString(got) {
			t.Errorf("PBSBackupID(%q,%q) = %q, which PBS will reject", tc.slug, tc.short, got)
		}
		if !strings.Contains(got, tc.short) {
			t.Errorf("PBSBackupID(%q,%q) = %q dropped the short id, so two servers could collide",
				tc.slug, tc.short, got)
		}
	}
}

// Prune with no --keep flag deletes the entire group. If a missing policy ever
// degrades into a bare prune, one scheduled backup wipes every snapshot the
// operator had — so "no policy" must be unrepresentable as a command.
func TestPBSPruneRefusesToRunWithoutAPolicy(t *testing.T) {
	if _, ok := PBSPruneArgs(Config{}, "srv", 0, 0); ok {
		t.Fatal("prune must not be built when no retention is configured")
	}
	args, ok := PBSPruneArgs(Config{}, "srv", 5, 0)
	if !ok {
		t.Fatal("keep_n=5 should produce a prune")
	}
	if !slices.Contains(args, "--keep-last") {
		t.Errorf("keep_n did not become --keep-last: %v", args)
	}
	if slices.Contains(args, "--keep-daily") {
		t.Errorf("keep_days was not set but --keep-daily appeared: %v", args)
	}
	both, _ := PBSPruneArgs(Config{}, "srv", 5, 14)
	if !slices.Contains(both, "--keep-last") || !slices.Contains(both, "--keep-daily") {
		t.Errorf("both rules should appear: %v", both)
	}
}

func TestPBSNamespaceFlows(t *testing.T) {
	cfg := Config{Namespace: "yggdrasil"}
	for name, args := range map[string][]string{
		"backup":  PBSBackupArgs(cfg, "srv", "/data", nil),
		"list":    PBSSnapshotListArgs(cfg, "srv"),
		"restore": PBSRestoreArgs(cfg, "host/srv/x", "/data"),
		"forget":  PBSForgetArgs(cfg, "host/srv/x"),
	} {
		if !slices.Contains(args, "--ns") {
			t.Errorf("%s dropped the namespace: %v", name, args)
		}
	}
	prune, _ := PBSPruneArgs(cfg, "srv", 3, 0)
	if !slices.Contains(prune, "--ns") {
		t.Errorf("prune dropped the namespace: %v", prune)
	}
	// Empty namespace means the datastore root; sending --ns "" is an error.
	if slices.Contains(PBSBackupArgs(Config{}, "srv", "/data", nil), "--ns") {
		t.Error("an unset namespace must not produce --ns")
	}
}

// A backup written under an encryption key the panel cannot reproduce is not a
// backup. The client defaults to "encrypt", so this must be explicit.
func TestPBSNeverEncryptsImplicitly(t *testing.T) {
	for name, args := range map[string][]string{
		"backup":  PBSBackupArgs(Config{}, "srv", "/data", nil),
		"restore": PBSRestoreArgs(Config{}, "host/srv/x", "/data"),
	} {
		i := slices.Index(args, "--crypt-mode")
		if i < 0 || i+1 >= len(args) || args[i+1] != "none" {
			t.Errorf("%s must pass --crypt-mode none: %v", name, args)
		}
	}
}

// Restoring in place hits an existing directory tree. Without both flags the
// client stops at the first subdirectory that already exists.
func TestPBSRestoreCanOverwriteALiveDataDir(t *testing.T) {
	args := PBSRestoreArgs(Config{}, "host/s/2026-09-02T18:00:00Z", "/data")
	for _, f := range []string{"--overwrite", "--allow-existing-dirs"} {
		i := slices.Index(args, f)
		if i < 0 || args[i+1] != "true" {
			t.Errorf("restore missing %s true: %v", f, args)
		}
	}
}

func TestPBSEnvPassesTheSecretByFileNotByValue(t *testing.T) {
	env := PBSEnv(Config{Username: "u@pbs!t", Host: "h", Datastore: "d",
		Password: "super-secret", Fingerprint: "aa:bb"}, "/run/pbs-pass")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "super-secret") {
		t.Error("the token secret reached the environment; `docker inspect` would show it")
	}
	if !strings.Contains(joined, "PBS_PASSWORD_FILE=/run/pbs-pass") {
		t.Errorf("no password file: %v", env)
	}
	if !strings.Contains(joined, "PBS_FINGERPRINT=aa:bb") {
		t.Errorf("fingerprint dropped, so a self-signed PBS would be refused: %v", env)
	}
	// The client writes tickets under $HOME; without one it fails in a way that
	// reads like bad credentials.
	if !strings.Contains(joined, "HOME=") {
		t.Errorf("no HOME: %v", env)
	}
}

func TestPBSEnvOmitsFingerprintWhenUnset(t *testing.T) {
	for _, e := range PBSEnv(Config{Host: "h", Datastore: "d"}, "/p") {
		if strings.HasPrefix(e, "PBS_FINGERPRINT=") {
			t.Errorf("empty fingerprint must not be sent: %q", e)
		}
	}
}

func TestParsePBSSnapshots(t *testing.T) {
	in := []byte(`[
	  {"backup-id":"srv","backup-time":1756000000,"backup-type":"host","size":1048576,"protected":false,"files":["data.pxar.didx"]},
	  {"backup-id":"srv","backup-time":1756100000,"backup-type":"host","protected":true,"files":["data.pxar.didx"]}
	]`)
	got, err := ParsePBSSnapshots(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(got))
	}
	// Newest first — the restore dialog shows this list in order.
	if !got[0].Time.After(got[1].Time) {
		t.Error("snapshots are not newest-first")
	}
	if got[0].Name != PBSSnapshotName("srv", time.Unix(1756100000, 0).UTC()) {
		t.Errorf("name %q is not what restore takes", got[0].Name)
	}
	if !got[0].Protected {
		t.Error("protected flag lost")
	}
	// size is optional in the PBS schema; a missing one is 0, not an error.
	if got[0].Size != 0 || got[1].Size != 1048576 {
		t.Errorf("sizes wrong: %d, %d", got[0].Size, got[1].Size)
	}
}

func TestParsePBSSnapshotsToleratesLeadingNoise(t *testing.T) {
	in := []byte("connecting to pbs.lan...\n[{\"backup-id\":\"s\",\"backup-time\":1756000000}]")
	got, err := ParsePBSSnapshots(in)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v — progress output must not break the listing", got, err)
	}
}

func TestParsePBSSnapshotsSkipsUnusableEntries(t *testing.T) {
	in := []byte(`[{"backup-id":"","backup-time":0},{"backup-id":"s","backup-time":1756000000}]`)
	got, _ := ParsePBSSnapshots(in)
	if len(got) != 1 {
		t.Errorf("one bad entry must not hide the good ones: %v", got)
	}
}

func TestParsePBSSnapshotsEmptyGroup(t *testing.T) {
	got, err := ParsePBSSnapshots([]byte(`[]`))
	if err != nil || len(got) != 0 {
		t.Errorf("an empty group is not an error: %v, %v", got, err)
	}
}

func TestPBSExcludes(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"world", "world_nether", "cache", "logs", "plugins"} {
		if err := os.Mkdir(filepath.Join(dir, n), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Only the included paths survive; everything else is named explicitly.
	ex, err := PBSExcludes(dir, []string{"world", "world_nether", "server.properties", "plugins"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/cache", "/logs"}
	if !slices.Equal(ex, want) {
		t.Errorf("got %v, want %v", ex, want)
	}
	// Anchored, so a nested directory of the same name is not swept up.
	for _, e := range ex {
		if !strings.HasPrefix(e, "/") {
			t.Errorf("exclude %q is not anchored at the archive root", e)
		}
	}
}

// include: ["."] is the common case and means the whole directory. Producing
// exclusions there would be wrong in the most damaging direction.
func TestPBSExcludesWholeDirectory(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "data"), 0755)
	for _, inc := range [][]string{nil, {"."}, {"./"}, {"/"}, {"world", "."}} {
		ex, err := PBSExcludes(dir, inc)
		if err != nil {
			t.Fatalf("%v: %v", inc, err)
		}
		if len(ex) != 0 {
			t.Errorf("include %v should exclude nothing, got %v", inc, ex)
		}
	}
}

// A nested include keeps its whole top-level parent: pxar excludes by path, and
// excluding "world" to keep "world/region" would remove the thing asked for.
func TestPBSExcludesKeepsParentOfANestedInclude(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "world"), 0755)
	os.Mkdir(filepath.Join(dir, "cache"), 0755)
	ex, err := PBSExcludes(dir, []string{"world/region"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ex, "/world") {
		t.Errorf("excluded the parent of an included path: %v", ex)
	}
	if !slices.Contains(ex, "/cache") {
		t.Errorf("should still exclude /cache: %v", ex)
	}
}

// PBS is not a stream target and must never be opened as one — an archive
// uploaded as a blob would dedupe against nothing.
func TestOpenRefusesPBS(t *testing.T) {
	if _, err := Open(Config{Type: PBSType}); err == nil {
		t.Fatal("Open(pbs) must fail rather than take the archive path")
	}
}

// Recorded from a real Proxmox Backup Server 4.0 via proxmox-backup-client
// 4.0.19, because the published API schema and the wire format disagree: the
// schema declares `files` as an array of strings and the client sends an array
// of objects. Decoding the documented shape fails and loses the entire listing.
// If this file ever needs regenerating, capture it from a live server —
// hand-written JSON would not have caught that.
func TestParsePBSSnapshotsAgainstRealServerOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "pbs_snapshot_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePBSSnapshots(raw)
	if err != nil {
		t.Fatalf("real PBS output must parse: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d snapshots, want 6", len(got))
	}
	for i, sn := range got {
		if !strings.HasPrefix(sn.Name, "host/test-server-a1b2c3d4/") {
			t.Errorf("snapshot %d name %q is not a restorable identifier", i, sn.Name)
		}
		if sn.Size <= 0 {
			t.Errorf("snapshot %d has no size", i)
		}
		if i > 0 && !got[i-1].Time.After(sn.Time) {
			t.Errorf("snapshots not strictly newest-first at %d", i)
		}
	}
}
