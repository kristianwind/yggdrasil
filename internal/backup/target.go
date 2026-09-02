package backup

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"
)

// Object describes a stored archive on a target.
type Object struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Target is a storage backend for archives.
type Target interface {
	Put(ctx context.Context, name string, r io.Reader) (int64, error)
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	List(ctx context.Context) ([]Object, error)
	Delete(ctx context.Context, name string) error
	Close() error
}

// Config is the decrypted configuration for opening a target.
//
// It is stored as encrypted JSON, so adding a field is backward compatible: a
// target written before the field existed decodes with it empty.
type Config struct {
	Type     string `json:"type"` // local | sftp | smb | pbs
	Path     string `json:"path"` // base directory / remote path / share subpath
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"` // for pbs: the auth-id, e.g. ygg@pbs!panel
	Password string `json:"password,omitempty"` // for pbs: the API token secret
	Share    string `json:"share,omitempty"`    // SMB share name

	// Proxmox Backup Server only.
	Datastore   string `json:"datastore,omitempty"`
	Namespace   string `json:"namespace,omitempty"`   // optional; empty = the datastore root
	Fingerprint string `json:"fingerprint,omitempty"` // server cert sha256, for self-signed installs
}

// Open connects/opens a target from its config.
func Open(cfg Config) (Target, error) {
	switch cfg.Type {
	case "local", "nfs", "cifs-mount":
		// "local" also covers NFS/CIFS shares already mounted on the host.
		return openLocal(cfg)
	case "sftp":
		return openSFTP(cfg)
	case "smb":
		return openSMB(cfg)
	case PBSType:
		// Not a Target and cannot be made into one: PBS stores deduplicated
		// chunks of a directory, not a named blob. It runs through its own
		// pipeline (see pbs.go). Reaching here means a caller took the archive
		// path for a PBS target, which would upload a tar.gz that dedupes
		// against nothing — so fail loudly instead of "working".
		return nil, fmt.Errorf("proxmox backup server is not a stream target; use the PBS pipeline")
	default:
		return nil, fmt.Errorf("unsupported backup target type %q", cfg.Type)
	}
}

// Retention selects which objects to delete given keep-N and keep-days rules.
// An object is kept if it is within the newest keepN OR newer than keepDays.
// keepN<=0 disables the count rule; keepDays<=0 disables the age rule.
func Retention(objects []Object, keepN, keepDays int, now time.Time) []Object {
	sorted := append([]Object(nil), objects...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ModTime.After(sorted[j].ModTime) })

	var toDelete []Object
	for i, o := range sorted {
		keep := false
		if keepN > 0 && i < keepN {
			keep = true
		}
		if keepDays > 0 && o.ModTime.After(now.AddDate(0, 0, -keepDays)) {
			keep = true
		}
		if keepN <= 0 && keepDays <= 0 {
			keep = true // no policy → keep everything
		}
		if !keep {
			toDelete = append(toDelete, o)
		}
	}
	return toDelete
}
