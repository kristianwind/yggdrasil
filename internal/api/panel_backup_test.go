package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/crypto"
	"github.com/kristianwind/yggdrasil/internal/panelbackup"
)

// Every server had backups; the thing that knows which servers exist, on which
// ports, behind which domains, with which credentials, had none.

func panelBackupFixture(t *testing.T) (*Server, string) {
	t.Helper()
	s := testServer(t)
	ciph, err := crypto.New("unit-test-key-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	s.cipher = ciph
	s.cfg = &config.Config{}
	s.cfg.Auth.SecretKey = "the-key-that-unlocks-every-credential"
	s.cfg.Database.Path = filepath.Join(t.TempDir(), "yggdrasil.db")

	dir := t.TempDir()
	cfgJSON, _ := json.Marshal(backup.Config{Type: "local", Path: dir})
	enc, err := ciph.Encrypt(string(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	tid := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO backup_targets (id, name, type, config_enc) VALUES (?,?,?,?)`,
		tid, "Attic NAS", "local", enc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	passEnc, _ := ciph.Encrypt("correct horse battery staple")
	s.setSetting(ctx, "panel_backup_target_id", tid)
	s.setSetting(ctx, "panel_backup_pass", passEnc)
	return s, dir
}

func archives(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error { //nolint:errcheck
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, panelBackupSuffix) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestPanelBackupWritesAnArchiveThatOpens(t *testing.T) {
	s, dir := panelBackupFixture(t)
	name, err := s.runPanelBackup(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(name, panelBackupPrefix) || !strings.HasSuffix(name, panelBackupSuffix) {
		t.Errorf("archive name %q does not carry the prefix retention scopes by", name)
	}

	files := archives(t, dir)
	if len(files) != 1 {
		t.Fatalf("archives on the target = %v, want exactly 1", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("archive is %d bytes", len(raw))

	// The number next to the green: it has to OPEN, with the passphrase, and
	// carry the key. A zero-byte file would also "be written".
	plain, err := panelbackup.Open(bytes.NewReader(raw), "correct horse battery staple")
	if err != nil {
		t.Fatalf("the archive does not open with the configured passphrase: %v", err)
	}
	// Read the manifest out of the bundle rather than grepping the bytes: the
	// plaintext is a gzip, so a substring search over it finds nothing whatever
	// is inside, and would have passed over an archive with no key at all.
	man := manifestFrom(t, plain)
	if man.SecretKey != "the-key-that-unlocks-every-credential" {
		t.Errorf("manifest secret_key = %q — without it the archive cannot be restored", man.SecretKey)
	}

	var status, path string
	var size int64
	if err := s.db.QueryRow(
		`SELECT status, COALESCE(path,''), COALESCE(size_bytes,0) FROM panel_backups ORDER BY created_at DESC LIMIT 1`).
		Scan(&status, &path, &size); err != nil {
		t.Fatalf("nothing was recorded: %v", err)
	}
	if status != "done" || path != name || size == 0 {
		t.Errorf("row = %q %q %d, want done/%s/non-zero", status, path, size, name)
	}
}

// Turning the schedule on without a passphrase would book a job that can only
// fail, every night, and report it.
// manifestFrom pulls manifest.json out of the plain (gzipped tar) bundle.
func manifestFrom(t *testing.T, plain []byte) struct {
	SecretKey string `json:"secret_key"`
	Servers   []struct {
		Name string `json:"name"`
	} `json:"servers"`
} {
	t.Helper()
	var out struct {
		SecretKey string `json:"secret_key"`
		Servers   []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("the archive plaintext is not a gzip bundle: %v", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "manifest.json" {
			if err := json.NewDecoder(tr).Decode(&out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatal("no manifest.json in the archive")
	return out
}

func TestPanelBackupRefusesWithoutAPassphrase(t *testing.T) {
	s, dir := panelBackupFixture(t)
	s.setSetting(context.Background(), "panel_backup_pass", "")
	if _, err := s.runPanelBackup(context.Background()); err == nil {
		t.Error("it ran without a passphrase")
	} else if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error = %q, want it to name the passphrase", err)
	}
	if f := archives(t, dir); len(f) != 0 {
		t.Errorf("it wrote %v anyway", f)
	}
}

// Retention is scoped to our own prefix and suffix. A backup target is a
// directory somebody else may also use, and "delete all but the newest few"
// over a shared folder is how a policy eats a neighbour's data.
func TestPanelRetentionKeepsTheNewestAndSpareseverythingElse(t *testing.T) {
	s, dir := panelBackupFixture(t)
	ctx := context.Background()
	s.setSetting(ctx, "panel_backup_keep", "2")

	// A neighbour's file, in the same folder, that retention must never touch.
	neighbour := filepath.Join(dir, "someone-elses-backup.tar.gz")
	if err := os.WriteFile(neighbour, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	// And one of ours under a different name, as a server backup would be.
	serverish := filepath.Join(dir, "shop-1a2b")
	os.MkdirAll(serverish, 0o755)                                                        //nolint:errcheck
	os.WriteFile(filepath.Join(serverish, "20260101-000000.tar.gz"), []byte("x"), 0o644) //nolint:errcheck

	var names []string
	for i := 0; i < 4; i++ {
		n, err := s.runPanelBackup(ctx)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		names = append(names, n)
	}

	left := archives(t, dir)
	t.Logf("kept %d of %d archives: %v", len(left), len(names), left)
	if len(left) != 2 {
		t.Fatalf("retention kept %d archives, want 2", len(left))
	}
	// Newest kept, not oldest. Keeping the wrong two is the failure that looks
	// identical until you need one.
	sort.Strings(names)
	newest := names[len(names)-1]
	var found bool
	for _, p := range left {
		if strings.HasSuffix(p, strings.TrimPrefix(newest, panelBackupPrefix)) {
			found = true
		}
	}
	if !found {
		t.Errorf("the newest archive %q was deleted; kept %v", newest, left)
	}

	if _, err := os.Stat(neighbour); err != nil {
		t.Error("retention deleted a file that was not ours")
	}
	if _, err := os.Stat(filepath.Join(serverish, "20260101-000000.tar.gz")); err != nil {
		t.Error("retention deleted a server backup")
	}
}

// keep=0 means no policy. It must not read as "keep nothing".
func TestPanelRetentionWithNoPolicyKeepsEverything(t *testing.T) {
	s, dir := panelBackupFixture(t)
	ctx := context.Background()
	s.setSetting(ctx, "panel_backup_keep", "0")
	for i := 0; i < 3; i++ {
		if _, err := s.runPanelBackup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f := archives(t, dir); len(f) != 3 {
		t.Errorf("kept %d archives with no retention policy, want all 3", len(f))
	}
}
