package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Running rclone for the consumer-cloud backup targets (see internal/backup/
// rclone.go). Same shape as the PBS client: one short-lived container per
// operation, argv passed directly, the credential delivered as a file copied
// into the container rather than through the environment or a bind mount.

// defaultRcloneImage is pinned so an upgrade is a reviewed commit, as with the
// PBS client.
const defaultRcloneImage = "rclone/rclone:1.75.2"

func (s *Server) rcloneImage() string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.Docker.RcloneImage) != "" {
		return strings.TrimSpace(s.cfg.Docker.RcloneImage)
	}
	return defaultRcloneImage
}

// rcloneRunner implements backup.RcloneRunner on the panel's Docker client.
type rcloneRunner struct{ s *Server }

func (r rcloneRunner) RunRclone(ctx context.Context, conf []byte, argv []string, stdin io.Reader, stdout io.Writer) ([]byte, string, error) {
	if r.s.docker == nil {
		return nil, "", errors.New("docker is not available")
	}
	if err := r.s.pullImage(ctx, r.s.rcloneImage(), nil); err != nil {
		return nil, "", err
	}
	var stderr bytes.Buffer
	files, err := r.s.docker.RunStream(ctx, docker.StreamOptions{
		Image: r.s.rcloneImage(),
		Argv:  append([]string{"rclone", "--config", backup.RcloneConfPath, "--retries", "3"}, argv...),
		Files: map[string][]byte{backup.RcloneConfPath: conf},
		// Read back even on failure: a token can be refreshed before the
		// operation that then fails.
		Collect: []string{backup.RcloneConfPath},
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  &limitedBuffer{b: &stderr, max: 64 << 10},
	})
	return files[backup.RcloneConfPath], stderr.String(), err
}

// limitedBuffer keeps the first max bytes of stderr; rclone's reason is short,
// and a verbose failure must not grow without bound.
type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

// rcloneLocks serialises runs per target. rclone refreshes an expired token
// and writes it back; two runs refreshing at once can each invalidate the
// other's refresh token, and the second save would then keep the dead one.
var rcloneLocks sync.Map // target id → *sync.Mutex

func rcloneLock(targetID string) *sync.Mutex {
	m, _ := rcloneLocks.LoadOrStore(targetID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// openTarget opens any stream target. Everything that reads or writes a
// target goes through here rather than backup.Open, because an rclone target
// needs Docker and somewhere to store the token it refreshes.
func (s *Server) openTarget(targetID string, cfg backup.Config) (backup.Target, error) {
	if cfg.Type != backup.RcloneType {
		return backup.Open(cfg)
	}
	var run backup.RcloneRunner
	if s.docker != nil {
		run = rcloneRunner{s}
	}
	return backup.OpenRclone(cfg, run, rcloneLock(targetID), func(conf []byte) {
		s.saveRcloneConf(targetID, conf)
	})
}

// saveRcloneConf stores a refreshed login. It re-reads the target first and
// changes only the login, so an edit saved in the meantime is not undone.
func (s *Server) saveRcloneConf(targetID string, conf []byte) {
	ctx := context.Background()
	cur, err := s.loadTargetConfig(ctx, targetID)
	if err != nil || cur.Type != backup.RcloneType {
		return
	}
	cur.RcloneConf = string(conf)
	enc, err := s.encryptTargetConfig(*cur)
	if err != nil {
		log.Printf("backup target %s: could not store the refreshed rclone login: %v", targetID, err)
		return
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE backup_targets SET config_enc=? WHERE id=?", enc, targetID); err != nil {
		log.Printf("backup target %s: could not store the refreshed rclone login: %v", targetID, err)
	}
}

// prepareRclone builds the login for an rclone target being saved. A blank
// credential on an edit keeps the existing login, unless the service changed.
func (s *Server) prepareRclone(ctx context.Context, cfg *backup.Config, cur *backup.Config) error {
	cfg.RcloneConf = "" // only ever built here, never taken from the request
	if strings.TrimSpace(cfg.Password) == "" && cur != nil && cur.Type == backup.RcloneType && cur.Provider == cfg.Provider && cur.RcloneConf != "" {
		cfg.RcloneConf = cur.RcloneConf
		return nil
	}
	var run backup.RcloneRunner
	if s.docker != nil {
		run = rcloneRunner{s}
	}
	conf, err := backup.RcloneSetup(ctx, run, *cfg)
	if err != nil {
		return err
	}
	cfg.RcloneConf = string(conf)
	// The credential has done its job: a Jottacloud login token is single-use,
	// and for the others the token is now inside the conf. Keep one copy.
	cfg.Password = ""
	return nil
}
