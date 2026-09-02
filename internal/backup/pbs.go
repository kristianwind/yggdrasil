package backup

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Proxmox Backup Server as a backup destination.
//
// PBS deliberately does NOT implement Target, and that is the most important
// thing in this file. Target is "here is a byte stream, store it under this
// name" — exactly what local, SFTP and SMB want, and exactly what PBS is not.
// PBS walks a DIRECTORY, splits it into content-defined chunks, and uploads only
// the chunks the datastore does not already hold. Handing it the finished
// .tar.gz that the archive pipeline produces would compile, would appear to
// work, and would destroy the entire reason to use PBS: gzip output is
// completely different after a one-byte change, so every nightly run would
// re-upload the whole archive and the deduplication ratio would sit at 1.0. The
// operator would pay PBS's costs and receive none of its benefits, and nothing
// in the UI would tell them.
//
// So PBS is a second kind of destination running alongside the archive pipeline
// rather than inside it:
//
//	archive targets   data dir -> tar.gz -> Put(name, stream)
//	PBS               data dir -> proxmox-backup-client backup data.pxar:/data
//
// Retention becomes `prune` (server-side, chunk-aware) instead of deleting
// objects, and restore streams a snapshot back down instead of unpacking a tar.
//
// The client itself is a single statically linked binary that Proxmox ships in
// its pbs-client repository, so it runs in a ~26 MB image with no host
// dependency — see deploy/pbs-client/Dockerfile. That matters: the panel is
// installed on machines we do not control, and "first install this apt repo" is
// a support burden we would carry forever.
//
// KNOWN LIMIT, stated once so it is not rediscovered: Proxmox builds the client
// for amd64 only. There is no arm64 package, so this destination cannot work on
// a Raspberry Pi panel. The UI says so rather than failing at the first backup.

// PBSType is the backup_targets.type value for this destination.
const PBSType = "pbs"

// PBSArchive is the single pxar archive every snapshot contains. One archive
// rather than one per included path, because backup.include lists FILES as well
// as directories (Minecraft includes server.properties) and a pxar source must
// be a directory. Inclusion is expressed as exclusions instead — see PBSExcludes.
const PBSArchive = "data"

// PBSDefaultPort is the port PBS listens on.
const PBSDefaultPort = 8007

// pbsIDSafe matches the backup-id pattern PBS enforces server-side:
// ^[A-Za-z0-9_][A-Za-z0-9._\-]*$. Failing this produces a rejection from the
// API rather than from the client, which reads like a permissions problem.
var pbsIDSafe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-]*$`)

// PBSBackupID converts a panel server name plus its short id into a legal PBS
// backup-id. Each panel server gets its own backup group, which is what makes
// the incremental upload work: PBS dedupes a snapshot against the previous
// snapshot in the SAME group, so a stable id per server is the whole trick.
func PBSBackupID(slug, shortID string) string {
	id := slug + "-" + shortID
	// slugName already yields [a-z0-9-]+ with the dashes trimmed, so this is a
	// belt-and-braces pass for names that arrive from anywhere else.
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), ".-")
	if out == "" || !pbsIDSafe.MatchString(out) {
		return "server-" + shortID
	}
	return out
}

// PBSRepository builds the repository string the client expects:
//
//	[[auth-id@]server[:port]:]datastore
//
// The auth-id itself contains "@" and, for an API token, "!". That is fine here
// and would not be fine in a shell — which is why every caller passes this as
// one argv element and never interpolates it into a command line.
//
// An IPv6 literal has to be bracketed or the colons are read as the port
// separator; a hostname that already looks bracketed is left alone.
func PBSRepository(cfg Config) string {
	host := strings.TrimSpace(cfg.Host)
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	var sb strings.Builder
	if id := strings.TrimSpace(cfg.Username); id != "" {
		sb.WriteString(id)
		sb.WriteByte('@')
	}
	if host != "" {
		sb.WriteString(host)
		if cfg.Port != 0 && cfg.Port != PBSDefaultPort {
			fmt.Fprintf(&sb, ":%d", cfg.Port)
		}
		sb.WriteByte(':')
	}
	sb.WriteString(strings.TrimSpace(cfg.Datastore))
	return sb.String()
}

// PBSEnv is the environment for a client invocation.
//
// The token secret is passed by FILE, not by value. An -e on the container would
// be readable from `docker inspect` for as long as Docker keeps the container's
// JSON on disk, which is longer than the backup and longer than anyone thinks.
// passFile is a 0600 file the caller creates and removes.
//
// HOME is set because the client writes tickets and known-fingerprints under
// $HOME/.config; with no HOME it fails in a way that reads like an auth error.
func PBSEnv(cfg Config, passFile string) []string {
	env := []string{
		"PBS_REPOSITORY=" + PBSRepository(cfg),
		"HOME=/tmp",
	}
	if passFile != "" {
		env = append(env, "PBS_PASSWORD_FILE="+passFile)
	}
	// Homelab PBS installs almost always have a self-signed certificate. Without
	// the fingerprint the client refuses to connect, and the message names the
	// certificate rather than the missing setting — so the UI asks for this up
	// front instead of letting the first backup fail.
	if fp := strings.TrimSpace(cfg.Fingerprint); fp != "" {
		env = append(env, "PBS_FINGERPRINT="+fp)
	}
	return env
}

// pbsNS appends the namespace flag when one is configured. An empty namespace
// means the datastore root, which is what PBS does by default.
func pbsNS(cfg Config, args []string) []string {
	if ns := strings.TrimSpace(cfg.Namespace); ns != "" {
		return append(args, "--ns", ns)
	}
	return args
}

// PBSConnectionCheckArgs is the connection test: list the backup groups in the
// datastore. One call exercises DNS, TLS, the certificate fingerprint, the
// auth-id, the token secret AND the datastore name, so a green result means a
// backup will reach the same place.
//
// It lists rather than asking for `status`, and that is measured rather than
// assumed: `status` needs privileges beyond the datastore and fails with a bare
// "permission check failed" for a token that can back up perfectly well. `list`
// needs Datastore.Audit|Datastore.Backup — exactly what a backup token has — so
// the test passes for every credential that can actually do the job, and fails
// for every one that cannot.
func PBSConnectionCheckArgs(cfg Config) []string {
	return pbsNS(cfg, []string{"proxmox-backup-client", "list", "--output-format", "json"})
}

// PBSBackupArgs builds the backup invocation. srcMount is where the caller has
// bind-mounted the server's data directory inside the container.
func PBSBackupArgs(cfg Config, backupID, srcMount string, excludes []string) []string {
	args := []string{
		"proxmox-backup-client", "backup",
		PBSArchive + ".pxar:" + srcMount,
		"--backup-type", "host",
		"--backup-id", backupID,
		// A data directory is one filesystem's worth of files. Following a
		// mountpoint into it would silently pull in whatever else is mounted
		// there, which for a bind-mounted host path can be very large.
		"--all-file-systems", "false",
		"--skip-lost-and-found", "true",
		// The client's default is "encrypt", which means "use the default key if
		// one exists". No key exists in this container, so the effective result
		// is already none — but a backup written under a key the panel cannot
		// reproduce is unrestorable, so this says so out loud rather than relying
		// on an empty HOME staying empty.
		"--crypt-mode", "none",
	}
	for _, e := range excludes {
		args = append(args, "--exclude", e)
	}
	return pbsNS(cfg, args)
}

// PBSSnapshotListArgs lists one server's snapshots.
func PBSSnapshotListArgs(cfg Config, backupID string) []string {
	return pbsNS(cfg, []string{
		"proxmox-backup-client", "snapshot", "list", PBSGroup(backupID),
		"--output-format", "json",
	})
}

// PBSForgetArgs removes one snapshot. Note that the space is not reclaimed until
// the PBS server runs garbage collection — chunks are shared, so nothing else
// could be safe. Callers must not report freed bytes from this.
func PBSForgetArgs(cfg Config, snapshot string) []string {
	return pbsNS(cfg, []string{"proxmox-backup-client", "snapshot", "forget", snapshot})
}

// PBSPruneArgs applies retention server-side.
//
// The panel's policy is keep_n ("keep the newest N") and keep_days ("keep
// anything newer than D days"). PBS has --keep-last, which is exactly keep_n,
// and --keep-daily, which is "keep the newest snapshot of each of the last D
// days" — close to keep_days but not identical: with two backups on one day it
// keeps one of them, and it counts days that HAVE a backup rather than calendar
// days. That difference is worth stating in the UI, and is why prune is only
// asked for the rules that were actually set.
//
// Returning ok=false means no policy is configured. Running prune with no --keep
// flag at all would delete every snapshot in the group, so this must never
// degrade into "just call prune".
func PBSPruneArgs(cfg Config, backupID string, keepN, keepDays int) (args []string, ok bool) {
	if keepN <= 0 && keepDays <= 0 {
		return nil, false
	}
	args = []string{"proxmox-backup-client", "prune", PBSGroup(backupID), "--output-format", "json"}
	if keepN > 0 {
		args = append(args, "--keep-last", fmt.Sprint(keepN))
	}
	if keepDays > 0 {
		args = append(args, "--keep-daily", fmt.Sprint(keepDays))
	}
	return pbsNS(cfg, args), true
}

// PBSRestoreArgs restores a snapshot's pxar archive into dstMount.
func PBSRestoreArgs(cfg Config, snapshot, dstMount string) []string {
	return pbsNS(cfg, []string{
		"proxmox-backup-client", "restore", snapshot, PBSArchive + ".pxar", dstMount,
		// Both are needed for restore-in-place, which is every restore the panel
		// performs: --overwrite for files that already exist, --allow-existing-dirs
		// for the directories above them. With only the first, a restore over a
		// live data directory fails on the first subdirectory.
		"--overwrite", "true",
		"--allow-existing-dirs", "true",
		"--crypt-mode", "none",
	})
}

// PBSGroup is the backup group a server's snapshots live in.
func PBSGroup(backupID string) string { return "host/" + backupID }

// PBSSnapshotName is the identifier `restore` and `forget` take.
func PBSSnapshotName(backupID string, at time.Time) string {
	return PBSGroup(backupID) + "/" + at.UTC().Format(time.RFC3339)
}

// pbsSnapshotJSON is the subset of PBS's SnapshotListItem the panel uses.
//
// Deliberately a SUBSET, and that is load-bearing. The published API schema
// declares `files` as an array of strings; the client actually emits an array of
// objects ({filename, size, crypt-mode}). Decoding into the documented shape
// fails outright and takes the whole listing with it — measured against a real
// PBS 4.0, not guessed. Fields this code does not use are therefore left out
// entirely, so the next divergence between the documentation and the wire costs
// nothing. Do not add a field here without a fixture from a real server.
//
// `size` is optional in the schema — a snapshot whose size the server has not
// computed reports nothing rather than zero, so it stays a pointer.
type pbsSnapshotJSON struct {
	BackupID   string `json:"backup-id"`
	BackupTime int64  `json:"backup-time"`
	BackupType string `json:"backup-type"`
	Size       *int64 `json:"size"`
	Protected  bool   `json:"protected"`
}

// PBSSnapshot is one restorable point in time.
type PBSSnapshot struct {
	Name      string    `json:"name"` // host/<id>/<rfc3339>, what restore takes
	Time      time.Time `json:"time"`
	Size      int64     `json:"size"`
	Protected bool      `json:"protected"`
}

// ParsePBSSnapshots reads `snapshot list --output-format json`, newest first.
//
// The client prints progress to stderr and JSON to stdout, but a mixed stream
// is easy to end up with, so this tolerates leading noise by seeking to the
// first "[". Anything that is not an object with a backup-time is skipped
// rather than failing the whole listing: one unreadable entry must not hide the
// snapshots an operator is trying to restore from.
func ParsePBSSnapshots(out []byte) ([]PBSSnapshot, error) {
	i := strings.IndexByte(string(out), '[')
	if i < 0 {
		return nil, fmt.Errorf("no snapshot list in client output")
	}
	var raw []pbsSnapshotJSON
	if err := json.Unmarshal(out[i:], &raw); err != nil {
		return nil, fmt.Errorf("parse snapshot list: %w", err)
	}
	var snaps []PBSSnapshot
	for _, r := range raw {
		if r.BackupTime <= 0 || r.BackupID == "" {
			continue
		}
		t := time.Unix(r.BackupTime, 0).UTC()
		var size int64
		if r.Size != nil {
			size = *r.Size
		}
		snaps = append(snaps, PBSSnapshot{
			Name:      PBSSnapshotName(r.BackupID, t),
			Time:      t,
			Size:      size,
			Protected: r.Protected,
		})
	}
	sort.Slice(snaps, func(a, b int) bool { return snaps[a].Time.After(snaps[b].Time) })
	return snaps, nil
}

// PBSExcludes turns a gameskill's backup.include into the exclusions that leave
// exactly those paths behind.
//
// pxar takes one source directory and a list of things to leave out, so "back up
// only these" has to be expressed as "leave out everything else". The list of
// "everything else" is read from the directory at backup time, which is honest
// about one edge: a top-level entry created between this read and the walk is
// included rather than excluded. For a backup that is the safe direction to err,
// and it beats the alternative of quietly ignoring backup.include — which is
// what a naive PBS implementation does, and which would silently back up a
// Minecraft server's entire 40 GB cache alongside its 200 MB world.
//
// An empty include list, or one containing ".", means the whole directory.
func PBSExcludes(dataDir string, include []string) ([]string, error) {
	if len(include) == 0 {
		return nil, nil
	}
	keep := map[string]bool{}
	for _, in := range include {
		c := path.Clean(strings.TrimSpace(strings.ReplaceAll(in, `\`, "/")))
		if c == "." || c == "/" || c == "" {
			return nil, nil // everything
		}
		// Only the first segment matters: excluding a top-level entry excludes
		// its subtree, and keeping one keeps its subtree.
		keep[strings.SplitN(strings.TrimPrefix(c, "/"), "/", 2)[0]] = true
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	var ex []string
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		// Anchored at the archive root so a name like "logs" excludes the
		// top-level logs directory and not every nested one.
		ex = append(ex, "/"+e.Name())
	}
	sort.Strings(ex)
	return ex, nil
}
