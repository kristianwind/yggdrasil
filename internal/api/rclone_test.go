package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// The whole rclone path against a real rclone, with Docker: setup through
// rclone's own non-interactive config, then what a backup, a restore and
// retention do. The service is WebDAV served by a second rclone container, the
// one provider that can run here without an account somewhere.
//
// Needs Docker and YGG_TEST_DOCKER=1; skipped otherwise.
func TestRcloneTargetAgainstWebDAV(t *testing.T) {
	if os.Getenv("YGG_TEST_DOCKER") == "" {
		t.Skip("YGG_TEST_DOCKER not set")
	}
	dc, err := docker.New("")
	if err != nil {
		t.Skip("no docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s := &Server{docker: dc}

	// A WebDAV server with a login, on the default bridge.
	name := "ygg-rclone-dav-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, defaultRcloneImage,
		"serve", "webdav", "/srv", "--addr", ":8080", "--user", "u", "--pass", "dav-pass").CombinedOutput(); err != nil {
		t.Fatalf("start webdav: %v %s", err, out)
	}
	defer exec.Command("docker", "stop", name).Run() //nolint:errcheck
	ipOut, err := exec.Command("docker", "inspect", "-f", "{{.NetworkSettings.Networks.bridge.IPAddress}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + strings.TrimSpace(string(ipOut)) + ":8080"
	time.Sleep(time.Second)

	cfg := backup.Config{Type: backup.RcloneType, Provider: "webdav", Host: url, Username: "u", Password: "dav-pass", Path: "panel/backups"}
	if err := s.prepareRclone(ctx, &cfg, nil); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if cfg.Password != "" {
		t.Error("the credential was kept beside the conf it was turned into")
	}
	if !strings.Contains(cfg.RcloneConf, "[ygg]") || strings.Contains(cfg.RcloneConf, "dav-pass") {
		t.Fatalf("conf = %q (want a [ygg] remote, with the password obscured by rclone)", cfg.RcloneConf)
	}

	var saved [][]byte
	tgt, err := backup.OpenRclone(cfg, rcloneRunner{s}, &sync.Mutex{}, func(b []byte) { saved = append(saved, b) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tgt.List(ctx); err != nil {
		t.Fatalf("list on a folder that does not exist yet: %v", err)
	}

	data := make([]byte, 3<<20+17)
	rand.Read(data) //nolint:errcheck
	pr, pw := io.Pipe()
	go func() { _, err := pw.Write(data); pw.CloseWithError(err) }()
	n, err := tgt.Put(ctx, "srv-1/20261028-120000.tar.gz", pr)
	if err != nil || n != int64(len(data)) {
		t.Fatalf("put = %d, %v", n, err)
	}
	if _, err := tgt.Put(ctx, "top.tar.gz", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}

	rc, err := tgt.Get(ctx, "srv-1/20261028-120000.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	back, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || sha256.Sum256(back) != sha256.Sum256(data) {
		t.Fatalf("read back %d bytes (err %v), want the %d written", len(back), err, len(data))
	}
	if _, err := tgt.Get(ctx, "srv-1/missing.tar.gz"); err == nil {
		t.Error("get of a missing archive did not fail up front")
	}

	objs, err := tgt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Name != "top.tar.gz" || objs[0].Size != 1 || objs[0].ModTime.IsZero() {
		t.Errorf("list = %+v, want only top.tar.gz (folders are not files)", objs)
	}
	for _, p := range []string{"srv-1/20261028-120000.tar.gz", "top.tar.gz"} {
		if err := tgt.Delete(ctx, p); err != nil {
			t.Errorf("delete %s: %v", p, err)
		}
	}
	if objs, _ := tgt.List(ctx); len(objs) != 0 {
		t.Errorf("after delete: %+v", objs)
	}
	if len(saved) != 0 {
		t.Errorf("conf saved %d times although nothing refreshed it", len(saved))
	}

	// A wrong password is refused with rclone's reason, at Test time.
	bad := backup.Config{Type: backup.RcloneType, Provider: "webdav", Host: url, Username: "u", Password: "wrong"}
	if err := s.prepareRclone(ctx, &bad, nil); err != nil {
		t.Fatal(err)
	}
	bt, _ := backup.OpenRclone(bad, rcloneRunner{s}, &sync.Mutex{}, nil)
	if _, err := bt.List(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
}

// What rclone writes into its config during a run comes back out of the
// container: this is the path a refreshed OAuth token takes.
func TestRcloneRunnerReturnsTheConfigRcloneWrote(t *testing.T) {
	if os.Getenv("YGG_TEST_DOCKER") == "" {
		t.Skip("YGG_TEST_DOCKER not set")
	}
	dc, err := docker.New("")
	if err != nil {
		t.Skip("no docker")
	}
	s := &Server{docker: dc}
	in := []byte("[ygg]\ntype = memory\n")
	out, stderr, err := rcloneRunner{s}.RunRclone(context.Background(), in,
		[]string{"config", "update", "ygg", "description", "refreshed-token-stand-in"}, nil, io.Discard)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if !bytes.Contains(out, []byte("refreshed-token-stand-in")) {
		t.Errorf("config after the run = %q", out)
	}
}

// A refreshed login is stored, and only the login: the rest of the target is
// re-read so an edit saved meanwhile survives. Without Docker, through a fake.
type fakeRclone struct{ next []byte }

func (f *fakeRclone) RunRclone(_ context.Context, conf []byte, argv []string, _ io.Reader, stdout io.Writer) ([]byte, string, error) {
	if argv[0] == "lsjson" {
		stdout.Write([]byte("[]")) //nolint:errcheck
	}
	return f.next, "", nil
}

func TestRcloneRefreshedLoginIsStored(t *testing.T) {
	s := transferTestServer(t, "rclone-refresh-test-key-0123456789")
	ctx := context.Background()
	cfg := backup.Config{Type: backup.RcloneType, Provider: "dropbox", RcloneConf: "[ygg]\ntype = dropbox\ntoken = old\n"}
	enc, _ := s.encryptTargetConfig(cfg)
	s.db.Exec("INSERT INTO backup_targets (id, name, type, config_enc, keep_n, keep_days) VALUES ('t1','Box','rclone',?,0,0)", enc) //nolint:errcheck

	// An edit lands between the open and the refresh.
	edited := cfg
	edited.Path = "moved"
	enc2, _ := s.encryptTargetConfig(edited)
	s.db.Exec("UPDATE backup_targets SET config_enc=? WHERE id='t1'", enc2) //nolint:errcheck

	fake := &fakeRclone{next: []byte("[ygg]\ntype = dropbox\ntoken = new\n")}
	tgt, err := backup.OpenRclone(cfg, fake, rcloneLock("t1"), func(b []byte) { s.saveRcloneConf("t1", b) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tgt.List(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.loadTargetConfig(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.RcloneConf, "token = new") {
		t.Errorf("refreshed token not stored: %q", got.RcloneConf)
	}
	if got.Path != "moved" {
		t.Errorf("storing the token undid an edit: path = %q", got.Path)
	}
}

// The login is built by the panel. One sent in a request is ignored, and an
// edit with the credential left blank keeps the login it has.
func TestPrepareRcloneKeepsAndIgnores(t *testing.T) {
	s := &Server{}
	cur := &backup.Config{Type: backup.RcloneType, Provider: "jottacloud", RcloneConf: "[ygg]\ntype = jottacloud\n"}
	in := backup.Config{Type: backup.RcloneType, Provider: "jottacloud", RcloneConf: "[ygg]\ntype = local\n"}
	if err := s.prepareRclone(context.Background(), &in, cur); err != nil {
		t.Fatal(err)
	}
	if in.RcloneConf != cur.RcloneConf {
		t.Errorf("conf = %q, want the stored one", in.RcloneConf)
	}
	// Changing the service needs a new login.
	sw := backup.Config{Type: backup.RcloneType, Provider: "dropbox"}
	if err := s.prepareRclone(context.Background(), &sw, cur); err == nil {
		t.Error("switching service without a new login was accepted")
	}
}
