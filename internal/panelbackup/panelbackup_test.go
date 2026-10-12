package panelbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
)

const testKey = "s3cr3t-key-that-unlocks-everything"

func fixture(t *testing.T) (*config.Config, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yggdrasil.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES ('s1','Shop','wordpress','/data/s1','running')`); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Auth.SecretKey = testKey
	cfg.Database.Path = path
	return cfg, database
}

// entries lists the file names inside the plain bundle, so a test can assert on
// what the archive actually carries rather than on its size.
func entries(t *testing.T, plain []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("plaintext is not a gzip bundle: %v", err)
	}
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
	return out
}

func TestRoundTripCarriesTheDatabaseAndTheKey(t *testing.T) {
	cfg, database := fixture(t)
	var buf bytes.Buffer
	if err := Create(cfg, database, "correct horse battery staple", &buf); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Named values, not "no error": an empty archive also decrypts cleanly.
	plain, err := Open(bytes.NewReader(buf.Bytes()), "correct horse battery staple")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	files := entries(t, plain)
	t.Logf("bundle holds %d entries: %v", len(files), keys(files))

	man, ok := files["manifest.json"]
	if !ok {
		t.Fatal("no manifest.json — nothing could be restored from this")
	}
	var m struct {
		SecretKey string `json:"secret_key"`
		DBFile    string `json:"db_file"`
		Servers   []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(man, &m); err != nil {
		t.Fatal(err)
	}
	// The whole reason the archive has to be encrypted.
	if m.SecretKey != testKey {
		t.Errorf("manifest secret_key = %q, want the panel's key — without it every stored credential is unreadable", m.SecretKey)
	}
	if len(m.Servers) != 1 || m.Servers[0].Name != "Shop" {
		t.Errorf("manifest servers = %+v, want the one seeded server", m.Servers)
	}
	var dbFound bool
	for name, body := range files {
		if strings.HasSuffix(name, ".db") {
			dbFound = true
			// A SQLite file, not an empty placeholder.
			if !bytes.HasPrefix(body, []byte("SQLite format 3")) {
				t.Errorf("%s is %d bytes and does not start with the SQLite magic", name, len(body))
			}
			if len(body) < 4096 {
				t.Errorf("%s is only %d bytes", name, len(body))
			}
		}
	}
	if !dbFound {
		t.Error("the bundle contains no database")
	}
}

// The passphrase is the only thing between the backup target and every
// credential the panel holds, so a wrong one must fail — and say which failure
// it is. "cipher: message authentication failed" is also what a damaged file
// gives, and those send an operator to different places.
func TestWrongPassphraseIsRefusedAndNamed(t *testing.T) {
	cfg, database := fixture(t)
	var buf bytes.Buffer
	if err := Create(cfg, database, "right", &buf); err != nil {
		t.Fatal(err)
	}
	_, err := Open(bytes.NewReader(buf.Bytes()), "wrong")
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("open with a wrong passphrase = %v, want ErrWrongPassphrase", err)
	}
}

// Every byte is authenticated, including the header. A salt edited in place
// must not decrypt onto something else.
func TestTamperedArchiveIsRefused(t *testing.T) {
	cfg, database := fixture(t)
	var buf bytes.Buffer
	if err := Create(cfg, database, "right", &buf); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		why string
		at  int
	}{
		{"a byte of the salt", len(magic) + 2},
		{"a byte of the nonce", len(magic) + saltLen + 2},
		{"a byte of the ciphertext", len(magic) + saltLen + 12 + 40},
	} {
		b := append([]byte{}, buf.Bytes()...)
		b[c.at] ^= 0xFF
		if _, err := Open(bytes.NewReader(b), "right"); err == nil {
			t.Errorf("flipping %s still opened the archive", c.why)
		}
	}
}

func TestAFileThatIsNotOursSaysSo(t *testing.T) {
	_, err := Open(strings.NewReader("just some bytes that are long enough to pass a length check......"), "x")
	if !errors.Is(err, ErrNotPanelBackup) {
		t.Errorf("open of a foreign file = %v, want ErrNotPanelBackup", err)
	}
}

// An archive anybody can open defeats the format. Refusing is the only safe
// answer: writing it and warning would leave the master key on a NAS.
func TestAnEmptyPassphraseIsRefused(t *testing.T) {
	cfg, database := fixture(t)
	var buf bytes.Buffer
	if err := Create(cfg, database, "", &buf); !errors.Is(err, ErrNoPassphrase) {
		t.Errorf("create with no passphrase = %v, want ErrNoPassphrase", err)
	}
	if buf.Len() != 0 {
		t.Errorf("it wrote %d bytes before refusing", buf.Len())
	}
}

// Two backups of the same database must not produce the same bytes: a reused
// salt or nonce would leak across archives, and an equal-bytes result is the
// visible symptom.
func TestEachArchiveIsSaltedAfresh(t *testing.T) {
	cfg, database := fixture(t)
	var a, b bytes.Buffer
	if err := Create(cfg, database, "same", &a); err != nil {
		t.Fatal(err)
	}
	if err := Create(cfg, database, "same", &b); err != nil {
		t.Fatal(err)
	}
	// Compared separately. The first version compared the whole header in one
	// slice, which the still-random nonce made different on its own — so it went
	// green over a salt that had been fixed to all zeroes.
	saltA := a.Bytes()[len(magic) : len(magic)+saltLen]
	saltB := b.Bytes()[len(magic) : len(magic)+saltLen]
	if bytes.Equal(saltA, saltB) {
		t.Errorf("two archives share a salt (%x) — the key is then the same for both", saltA)
	}
	var zero [saltLen]byte
	if bytes.Equal(saltA, zero[:]) {
		t.Error("the salt is all zeroes; it is not being generated")
	}
	nonceA := a.Bytes()[len(magic)+saltLen : len(magic)+saltLen+12]
	nonceB := b.Bytes()[len(magic)+saltLen : len(magic)+saltLen+12]
	if bytes.Equal(nonceA, nonceB) {
		t.Errorf("two archives share a nonce (%x)", nonceA)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
