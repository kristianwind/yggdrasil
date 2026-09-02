package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Running the Proxmox backup client.
//
// The client is a single statically linked binary, so it ships as a ~26 MB
// image rather than as a host dependency. That choice is deliberate: the panel
// is installed on machines we do not control, and "first add the Proxmox apt
// repository" is a support burden carried forever, on every OS the panel runs
// on. Docker is already a hard requirement here — using it costs nothing new.
//
// Everything below runs the client the same way: one ephemeral container, argv
// passed directly (never a shell — a Proxmox API token id contains "!"), the
// secret delivered through a mounted 0600 file, and the container removed on
// the way out.

// defaultPBSImage is the client image. Pinned to an exact client version rather
// than :latest so an upgrade is a commit somebody reviewed, and so a datastore
// written by one version is never silently read by another.
const defaultPBSImage = "ghcr.io/kristianwind/yggdrasil-pbs-client:4.0.19"

// pbsPassMount is where the token secret file appears inside the container.
const pbsPassMount = "/run/pbs"

// pbsSrcMount is where the server's data directory appears inside the container.
// Not /data: RunEphemeralOpts sets /data as the working directory, and having
// the pxar source be the cwd makes the archive's root ambiguous to read.
const pbsSrcMount = "/backup-src"

func (s *Server) pbsImage() string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.Docker.PBSClientImage) != "" {
		return strings.TrimSpace(s.cfg.Docker.PBSClientImage)
	}
	return defaultPBSImage
}

// pbsStagingRoot is where the short-lived credential file is written.
//
// It has to be a path the DOCKER DAEMON can resolve, which rules out /tmp: the
// panel's unit sets PrivateTmp=yes, so the panel and the daemon do not share
// one. Derived from the database path exactly the way server data directories
// are (handlers_servers.go), so it is the same root the daemon already binds
// from every day.
//
// Returning "" falls back to os.MkdirTemp's default, which is correct for tests
// and for anything not running under that unit.
func (s *Server) pbsStagingRoot() string {
	if s.cfg == nil || s.cfg.Database.Path == "" {
		return ""
	}
	root := filepath.Join(filepath.Dir(s.cfg.Database.Path), "tmp")
	if err := os.MkdirAll(root, 0700); err != nil {
		return ""
	}
	// Sweep anything a crashed run left behind. The normal path removes its own
	// directory, but a panel killed mid-backup would otherwise leave a file
	// containing a live PBS token sitting here forever. 0700 on the parent keeps
	// it away from other accounts either way; this keeps it from accumulating.
	if entries, err := os.ReadDir(root); err == nil {
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "ygg-pbs-") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
	}
	return root
}

// pbsSupported reports whether this machine can run the client at all.
//
// Proxmox publishes the backup client for amd64 only — there is no arm64 build
// in the pbs-client repository. On a Raspberry Pi panel every PBS operation
// would fail with an exec-format error, which reads like a corrupt image. Saying
// so up front is the difference between an unavailable feature and a broken one.
func pbsSupported() bool { return runtime.GOARCH == "amd64" }

const pbsUnsupportedMsg = "Proxmox publishes the backup client for amd64 only, " +
	"so a Proxmox Backup Server target cannot run on this panel's architecture (" + runtime.GOARCH + ")."

// pbsOpts is the per-invocation shape that differs between operations.
type pbsOpts struct {
	// SrcDir is bind-mounted at pbsSrcMount. Empty means no data mount (status).
	SrcDir string
	// Writable makes that mount read-write. Only restore needs it: a backup has
	// no business writing to the directory it is reading, and the kernel is a
	// better guarantee of that than intent.
	Writable bool
}

// pbsRun executes one client invocation and returns its combined output.
//
// The output is returned on failure too, and that matters: the client's exit
// code says only "1", while the reason ("authentication failed", "fingerprint
// mismatch", "datastore not found") is in the text. Discarding it would turn
// every PBS problem into the same unactionable error in the UI.
func (s *Server) pbsRun(ctx context.Context, cfg backup.Config, args []string, opts pbsOpts) ([]byte, error) {
	if !pbsSupported() {
		return nil, fmt.Errorf("%s", pbsUnsupportedMsg)
	}

	// The secret goes in a file, not in the environment: an -e would sit in
	// `docker inspect` output for as long as Docker keeps the container's JSON,
	// which outlives the backup. 0700 dir, 0600 file, both removed on the way out.
	//
	// NOT under /tmp, and that is the whole reason this helper exists. The panel
	// runs as a systemd service with PrivateTmp=yes, so its /tmp is a private
	// mount namespace. A path created there is real to the panel and does not
	// exist for the Docker daemon, which lives in the host namespace — the daemon
	// answers with `invalid mount config for type "bind": bind source path does
	// not exist`, naming a path the operator can see the panel just created. Hit
	// in production on the first real PBS backup.
	//
	// The panel's own state directory is the safe place: every server's data dir
	// is bind-mounted out of it many times a day, which is proof the daemon can
	// see it.
	dir, err := os.MkdirTemp(s.pbsStagingRoot(), "ygg-pbs-")
	if err != nil {
		return nil, fmt.Errorf("staging directory: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	passFile := filepath.Join(dir, "pass")
	// The client reads the whole file, so a stray newline would become part of
	// the secret and produce an authentication failure that looks like a typo.
	if err := os.WriteFile(passFile, []byte(strings.TrimSpace(cfg.Password)), 0600); err != nil {
		return nil, fmt.Errorf("staging credentials: %w", err)
	}

	eo := docker.EphemeralOptions{
		Image: s.pbsImage(),
		Argv:  args,
		Env:   backup.PBSEnv(cfg, pbsPassMount+"/pass"),
		// Root, so the client can read files owned by whatever uid the rune runs
		// as and can restore ownership faithfully on the way back. Combined with
		// a read-only source mount for backups, root here can read everything and
		// change nothing.
		User:           "0:0",
		ReadOnlyMounts: map[string]string{dir: pbsPassMount},
	}
	if opts.SrcDir != "" {
		eo.ExtraMounts = map[string]string{opts.SrcDir: pbsSrcMount}
		if !opts.Writable {
			eo.ReadOnlyMounts[opts.SrcDir] = pbsSrcMount
			delete(eo.ExtraMounts, opts.SrcDir)
		}
	}

	var out bytes.Buffer
	runErr := s.docker.RunEphemeralOpts(ctx, eo, &out)
	return out.Bytes(), runErr
}

// pbsError turns a failed invocation into something an operator can act on.
// The client's own message is the useful part; the exit code is not.
func pbsError(what string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	// Keep the tail: progress lines come first, the reason comes last.
	if len(msg) > 600 {
		msg = "…" + msg[len(msg)-600:]
	}
	if msg == "" {
		return fmt.Errorf("%s: %v", what, err)
	}
	return fmt.Errorf("%s: %s", what, msg)
}

// pbsEnsureImage pulls the client image if it is missing. Doing it once, up
// front, keeps a first-run pull from being reported as a backup failure.
func (s *Server) pbsEnsureImage(ctx context.Context) error {
	return s.docker.PullImage(ctx, s.pbsImage(), nil)
}

// pbsBackupID is the PBS backup group for a panel server: stable for the life of
// the server, because PBS deduplicates a snapshot against the previous snapshot
// in the same group. Derived from the name and the short id rather than stored,
// so it matches the naming the other targets already use.
func (s *Server) pbsBackupID(serverID string) string {
	short := serverID
	if len(short) > 8 {
		short = short[:8]
	}
	return backup.PBSBackupID(slugName(s.serverName(serverID)), short)
}

// runPBSBackup is the PBS half of runBackup: the data directory goes straight up
// as a pxar archive, so only the chunks that changed since the last snapshot are
// transferred. Returns the snapshot identifier and its size.
func (s *Server) runPBSBackup(ctx context.Context, cfg backup.Config, serverID, dataDir string, include []string) (snapshot string, size int64, err error) {
	if err := s.pbsEnsureImage(ctx); err != nil {
		return "", 0, fmt.Errorf("pull %s: %w", s.pbsImage(), err)
	}
	excludes, err := backup.PBSExcludes(dataDir, include)
	if err != nil {
		return "", 0, fmt.Errorf("read data directory: %w", err)
	}
	id := s.pbsBackupID(serverID)

	// Record the time before the run, not after: PBS names the snapshot from the
	// moment the backup STARTED, so a long backup would otherwise be looked up
	// under a timestamp that does not exist.
	started := time.Now().UTC().Truncate(time.Second)

	out, err := s.pbsRun(ctx, cfg, backup.PBSBackupArgs(cfg, id, pbsSrcMount, excludes), pbsOpts{SrcDir: dataDir})
	if err != nil {
		return "", 0, pbsError("backup", out, err)
	}

	// Ask the server which snapshot was actually created rather than trusting the
	// local clock. Clock skew of a second between the panel and PBS is normal and
	// would produce a name that restore cannot resolve.
	snap, size := s.pbsNewestSnapshot(ctx, cfg, id, started)
	if snap == "" {
		// The backup succeeded; only the lookup failed. Fall back to the local
		// name rather than reporting a failed backup that is sitting on the server.
		snap = backup.PBSSnapshotName(id, started)
	}
	return snap, size, nil
}

// pbsNewestSnapshot returns the newest snapshot at or after notBefore.
func (s *Server) pbsNewestSnapshot(ctx context.Context, cfg backup.Config, backupID string, notBefore time.Time) (string, int64) {
	snaps, err := s.pbsSnapshots(ctx, cfg, backupID)
	if err != nil {
		return "", 0
	}
	for _, sn := range snaps { // newest first
		if !sn.Time.Before(notBefore.Add(-2 * time.Minute)) {
			return sn.Name, sn.Size
		}
	}
	return "", 0
}

// pbsSnapshots lists a server's snapshots, newest first. A group that does not
// exist yet is an empty list, not an error — that is a server that has never
// been backed up.
func (s *Server) pbsSnapshots(ctx context.Context, cfg backup.Config, backupID string) ([]backup.PBSSnapshot, error) {
	out, err := s.pbsRun(ctx, cfg, backup.PBSSnapshotListArgs(cfg, backupID), pbsOpts{})
	if err != nil {
		return nil, pbsError("list snapshots", out, err)
	}
	// A server that has never been backed up lists as "[]" with exit 0 — no
	// special case needed here, unlike prune and forget below. Measured.
	return backup.ParsePBSSnapshots(out)
}

// pbsGroupMissing recognises "this server has no snapshots yet".
//
// Measured against PBS 4.0 rather than guessed, because the three commands do
// not agree:
//
//	snapshot list <unknown group>  ->  []          and exit 0
//	prune / snapshot forget        ->  unable to read ".../owner" - No such
//	                                   file or directory (os error 2), exit 1
//
// So listing needs no special case at all, and only the mutating commands do.
// Matching on the message is unavoidable — the client returns exit 1 for every
// failure — but it is deliberately ONE narrow string rather than a net of
// plausible-looking ones, so a real failure is never swallowed as "no snapshots
// yet". Anything broader risks reporting a permissions or network problem as
// success, which for a backup is the worst possible direction to be wrong in.
func pbsGroupMissing(out []byte) bool {
	return strings.Contains(strings.ToLower(string(out)), "no such file or directory")
}

// pbsPrune applies retention on the server, where it can be done chunk-aware.
func (s *Server) pbsPrune(ctx context.Context, cfg backup.Config, backupID string, keepN, keepDays int) error {
	args, ok := backup.PBSPruneArgs(cfg, backupID, keepN, keepDays)
	if !ok {
		return nil // no policy: keep everything, which is what prune-with-no-rules would NOT do
	}
	out, err := s.pbsRun(ctx, cfg, args, pbsOpts{})
	if err != nil {
		if pbsGroupMissing(out) {
			return nil
		}
		return pbsError("prune", out, err)
	}
	return nil
}

// pbsForget removes one snapshot. Note for callers: the space is not reclaimed
// until the PBS server runs garbage collection, because chunks are shared. Never
// report freed bytes from this.
func (s *Server) pbsForget(ctx context.Context, cfg backup.Config, snapshot string) error {
	out, err := s.pbsRun(ctx, cfg, backup.PBSForgetArgs(cfg, snapshot), pbsOpts{})
	if err != nil {
		if pbsGroupMissing(out) {
			return nil
		}
		return pbsError("forget snapshot", out, err)
	}
	return nil
}

// pbsRestore streams a snapshot back into a server's data directory.
func (s *Server) pbsRestore(ctx context.Context, cfg backup.Config, snapshot, dataDir string) error {
	if err := s.pbsEnsureImage(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", s.pbsImage(), err)
	}
	out, err := s.pbsRun(ctx, cfg, backup.PBSRestoreArgs(cfg, snapshot, pbsSrcMount),
		pbsOpts{SrcDir: dataDir, Writable: true})
	if err != nil {
		return pbsError("restore", out, err)
	}
	return nil
}

// pbsTest is the connection check — see PBSConnectionCheckArgs for why it lists
// rather than asking for status.
func (s *Server) pbsTest(ctx context.Context, cfg backup.Config) error {
	if err := s.pbsEnsureImage(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", s.pbsImage(), err)
	}
	out, err := s.pbsRun(ctx, cfg, backup.PBSConnectionCheckArgs(cfg), pbsOpts{})
	if err != nil {
		return pbsError("connect", out, err)
	}
	return nil
}

// applyPBSRetention is the PBS half of applyRetention.
//
// The archive targets delete objects the panel chose; PBS prunes server-side,
// where it can account for chunks shared between snapshots. The panel then
// reconciles: any backups row whose snapshot no longer exists on the server is
// removed, so the list in the UI is what is actually restorable rather than what
// was once created. Without that step a pruned snapshot stays on screen and
// fails only when somebody tries to restore it — during an incident.
func (s *Server) applyPBSRetention(ctx context.Context, serverID, targetID string, cfg backup.Config) {
	defer recoverLog("applyPBSRetention")
	var keepN, keepDays int
	s.db.QueryRow("SELECT keep_n, keep_days FROM backup_targets WHERE id=?", targetID).Scan(&keepN, &keepDays)
	if keepN <= 0 && keepDays <= 0 {
		return
	}
	id := s.pbsBackupID(serverID)
	if err := s.pbsPrune(ctx, cfg, id, keepN, keepDays); err != nil {
		log.Printf("pbs retention for %s: %v", s.serverName(serverID), err)
		// Say this out loud rather than only in the log. Pruning needs
		// Datastore.Prune, which the DatastoreBackup role does NOT carry — a
		// token that backs up perfectly well can be unable to delete anything.
		// Failing quietly means retention never runs, the datastore grows without
		// limit, and the first sign is a full disk. Measured against PBS 4.0:
		// "permission check failed - missing Datastore.Modify|Datastore.Prune".
		s.notifyServer(serverID, "⚠️ Backup kept, but retention could not run on "+
			"Proxmox Backup Server: "+err.Error())
		return
	}

	// Reconcile against what survived. If the listing fails, leave the rows
	// alone: deleting history because one call errored would be worse than
	// showing a snapshot that has gone.
	snaps, err := s.pbsSnapshots(ctx, cfg, id)
	if err != nil {
		log.Printf("pbs retention reconcile for %s: %v", s.serverName(serverID), err)
		return
	}
	alive := make(map[string]bool, len(snaps))
	for _, sn := range snaps {
		alive[sn.Name] = true
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, COALESCE(path,'') FROM backups WHERE server_id=? AND target_id=? AND status='done'",
		serverID, targetID)
	if err != nil {
		return
	}
	var gone []string
	for rows.Next() {
		var bid, p string
		if rows.Scan(&bid, &p) == nil && p != "" && !alive[p] {
			gone = append(gone, bid)
		}
	}
	rows.Close()
	for _, bid := range gone {
		s.db.ExecContext(ctx, "DELETE FROM backups WHERE id=?", bid)
	}
	if len(gone) > 0 {
		log.Printf("pbs retention: pruned %d snapshot(s) for %s", len(gone), s.serverName(serverID))
	}
}
