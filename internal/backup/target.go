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
type Config struct {
	Type     string `json:"type"` // local | sftp | smb
	Path     string `json:"path"` // base directory / remote path / share subpath
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Share    string `json:"share,omitempty"` // SMB share name
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
