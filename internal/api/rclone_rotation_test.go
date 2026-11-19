package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/backup"
)

// rotatingProvider behaves like Jottacloud: a refresh token works once, and
// using it hands back a new one while killing the old. A run with a dead token
// fails with invalid_grant — and rclone may still rewrite its config file, so
// the fake returns the (dead) config reformatted, to see whether anything
// stores it.
type rotatingProvider struct {
	mu    sync.Mutex
	valid int
	runs  int
}

var tokenRe = regexp.MustCompile(`token = (\d+)`)

func (p *rotatingProvider) RunRclone(_ context.Context, conf []byte, argv []string, _ io.Reader, stdout io.Writer) ([]byte, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs++
	m := tokenRe.FindSubmatch(conf)
	var got int
	if m != nil {
		fmt.Sscan(string(m[1]), &got) //nolint:errcheck
	}
	if got != p.valid {
		return append(conf, '\n'), "Failed to create file system: couldn't fetch token: invalid_grant: maybe token expired?", errors.New("exit 1")
	}
	p.valid++
	if argv[0] == "lsjson" {
		stdout.Write([]byte("[]")) //nolint:errcheck
	}
	return []byte(fmt.Sprintf("[ygg]\ntype = jottacloud\ntoken = %d\n", p.valid)), "", nil
}

func TestTwoTargetsOnOneSingleUseLoginDoNotKillEachOther(t *testing.T) {
	s := transferTestServer(t, "rclone-rotation-test-key-0123456789")
	ctx := context.Background()
	cfg := backup.Config{Type: backup.RcloneType, Provider: "jottacloud", RcloneConf: "[ygg]\ntype = jottacloud\ntoken = 1\n"}
	enc, _ := s.encryptTargetConfig(cfg)
	s.db.Exec("INSERT INTO backup_targets (id, name, type, config_enc, keep_n, keep_days) VALUES ('jt','Jotta','rclone',?,0,0)", enc) //nolint:errcheck

	prov := &rotatingProvider{valid: 1}
	s.rcloneRun = prov
	// Through the same function every backup, restore and retention pass uses.
	open := func() backup.Target {
		tg, err := s.openTarget("jt", cfg)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	// Two nightly backups, both opened from the login as it was at 02:00.
	a, b := open(), open()
	for i, tg := range []backup.Target{a, b, a, b} {
		if _, err := tg.List(ctx); err != nil {
			t.Fatalf("run %d failed: %v", i+1, err)
		}
	}
	got, _ := s.loadTargetConfig(ctx, "jt")
	if !strings.Contains(got.RcloneConf, fmt.Sprintf("token = %d", prov.valid)) {
		t.Fatalf("stored login %q is not the live one (token %d) — the next run would fail", got.RcloneConf, prov.valid)
	}
	t.Logf("%d runs, token rotated to %d, stored login is the live one", prov.runs, prov.valid)
}
