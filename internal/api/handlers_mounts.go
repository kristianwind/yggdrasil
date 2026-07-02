package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// hostMount is an admin-configured bind of a host path into a server's container
// (e.g. a media library). RW omitted = read-only (the safe default). Stored as a
// JSON array in servers.host_mounts.
type hostMount struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	RW        bool   `json:"rw"`
}

// hostMountSourceDenylist: panel-host paths a bind source may not be (or live under).
// Mounting these into a semi-trusted container would expose or endanger the host.
var hostMountSourceDenylist = []string{
	"/", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/etc", "/root", "/boot",
	"/proc", "/sys", "/dev", "/run", "/var/run", "/var/lib/yggdrasil", "/var/lib/docker",
}

// hostMountTargetDenylist: container paths a mount may not shadow (would break the
// image's own filesystem) — plus /data, which is the panel's own data mount.
var hostMountTargetDenylist = []string{
	"/", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/etc", "/proc", "/sys", "/dev", "/boot", "/data",
}

func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if p == "/" {
			if path == "/" { // "/" is a prefix of everything — match only the root itself
				return true
			}
			continue
		}
		if path == p || strings.HasPrefix(path+"/", p+"/") {
			return true
		}
	}
	return false
}

// validateHostMounts cleans and checks a set of admin-supplied host mounts. The
// source must be an existing directory outside the denylist; the target an
// absolute container path that doesn't shadow the system or the data mount.
func (s *Server) validateHostMounts(ms []hostMount) ([]hostMount, error) {
	// Derive the panel's data-dir root from the configured DB path so a
	// non-default Database.Path (e.g. /opt/ygg/ygg.db) still denies mounting the
	// SQLite DB, auth data, and every other server's files into a container.
	denied := append([]string{}, hostMountSourceDenylist...)
	if dbDir := filepath.Dir(s.cfg.Database.Path); dbDir != "" && dbDir != "/" && dbDir != "." {
		denied = append(denied, dbDir)
	}
	out := []hostMount{}
	seen := map[string]bool{}
	for _, m := range ms {
		host := filepath.Clean(strings.TrimSpace(m.Host))
		ctr := filepath.Clean(strings.TrimSpace(m.Container))
		if host == "" && ctr == "" {
			continue
		}
		if !filepath.IsAbs(host) {
			return nil, fmt.Errorf("host path %q must be absolute", m.Host)
		}
		if !filepath.IsAbs(ctr) {
			return nil, fmt.Errorf("container path %q must be absolute", m.Container)
		}
		if strings.Contains(host, "..") || strings.Contains(ctr, "..") {
			return nil, fmt.Errorf("paths must not contain ..")
		}
		if underAny(host, denied) {
			return nil, fmt.Errorf("host path %q is not allowed (sensitive system location)", host)
		}
		if underAny(ctr, hostMountTargetDenylist) {
			return nil, fmt.Errorf("container path %q is not allowed (shadows a system or data path)", ctr)
		}
		fi, err := os.Stat(host)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("host path %q must be an existing directory on the panel host", host)
		}
		// Resolve symlinks and re-check the denylist against the REAL target.
		// filepath.Clean alone can't see through a symlink (e.g. /srv/media ->
		// /etc or /var/lib/yggdrasil), which would otherwise smuggle a sensitive
		// host path past the string-level denylist. Bind the resolved path.
		resolved, err := filepath.EvalSymlinks(host)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve host path %q", m.Host)
		}
		if underAny(resolved, denied) {
			return nil, fmt.Errorf("host path %q resolves to a sensitive system location", m.Host)
		}
		host = resolved
		if seen[ctr] {
			return nil, fmt.Errorf("duplicate container path %q", ctr)
		}
		seen[ctr] = true
		out = append(out, hostMount{Host: host, Container: ctr, RW: m.RW})
	}
	return out, nil
}

// loadHostMounts reads a server's stored host mounts as docker bind specs
// (read-only unless explicitly writable).
func (s *Server) loadHostMounts(ctx context.Context, serverID string) []docker.HostMount {
	var raw string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(host_mounts,'') FROM servers WHERE id=?", serverID).Scan(&raw)
	if raw == "" {
		return nil
	}
	var ms []hostMount
	if json.Unmarshal([]byte(raw), &ms) != nil {
		return nil
	}
	out := make([]docker.HostMount, 0, len(ms))
	for _, m := range ms {
		if m.Host == "" || m.Container == "" {
			continue
		}
		out = append(out, docker.HostMount{Source: m.Host, Target: m.Container, ReadOnly: !m.RW})
	}
	return out
}
