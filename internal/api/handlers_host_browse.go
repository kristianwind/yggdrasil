package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Host browser — an admin-only, read-only view of the panel host's filesystem, so a
// host bind-mount source can be picked from a list instead of typed blind. Two
// layers: the mounted drives (from /proc/mounts) and a directory listing jailed to
// exactly what a bind mount is allowed to use (the same denylist validateHostMounts
// enforces). It only ever lists directories — never file contents, never writes.

// pseudoFS are kernel/virtual filesystems that aren't real "drives".
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "tmpfs": true,
	"devtmpfs": true, "devpts": true, "mqueue": true, "overlay": true, "bpf": true,
	"tracefs": true, "debugfs": true, "securityfs": true, "pstore": true, "hugetlbfs": true,
	"configfs": true, "fusectl": true, "binfmt_misc": true, "nsfs": true, "ramfs": true,
	"autofs": true, "rpc_pipefs": true, "efivarfs": true, "selinuxfs": true,
}

type hostDrive struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Fstype     string `json:"fstype"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// handleHostMountsList returns the host's real mounted drives (admin only).
func (s *Server) handleHostMountsList(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		jsonOK(w, []hostDrive{}) // not Linux / unreadable — an empty list, not an error
		return
	}
	seen := map[string]bool{}
	out := []hostDrive{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || pseudoFS[f[2]] {
			continue
		}
		mp := unescapeMount(f[1])
		if seen[mp] {
			continue
		}
		seen[mp] = true
		free, total := diskUsage(mp)
		out = append(out, hostDrive{Device: unescapeMount(f[0]), Mountpoint: mp, Fstype: f[2], TotalBytes: total, FreeBytes: free})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	jsonOK(w, out)
}

type hostEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// handleHostBrowse lists the sub-entries of a host directory, jailed to paths a bind
// mount is allowed to use (admin only, directories emphasized, read-only).
func (s *Server) handleHostBrowse(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(strings.TrimSpace(r.URL.Query().Get("path")))
	if path == "" || !filepath.IsAbs(path) {
		jsonError(w, "an absolute path is required", http.StatusBadRequest)
		return
	}
	if strings.Contains(path, "..") {
		jsonError(w, "path must not contain ..", http.StatusBadRequest)
		return
	}
	// Resolve symlinks so a link can't smuggle a denied location past the check.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	denied := append([]string{}, hostMountSourceDenylist...)
	if dbDir := filepath.Dir(s.cfg.Database.Path); dbDir != "" && dbDir != "/" && dbDir != "." {
		denied = append(denied, dbDir)
	}
	if underAny(path, denied) {
		jsonError(w, "that location can't be browsed (sensitive system path)", http.StatusForbidden)
		return
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		jsonError(w, "cannot read directory: "+err.Error(), http.StatusBadRequest)
		return
	}
	dirs := []hostEntry{}
	files := []hostEntry{}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // hide dotfiles/dirs; they're rarely a mount target and reduce noise
		}
		isDir := e.IsDir()
		if !isDir {
			if fi, ferr := e.Info(); ferr == nil && fi.Mode()&os.ModeSymlink != 0 {
				if st, serr := os.Stat(filepath.Join(path, name)); serr == nil {
					isDir = st.IsDir()
				}
			}
		}
		ent := hostEntry{Name: name, Path: filepath.Join(path, name), IsDir: isDir}
		if isDir {
			dirs = append(dirs, ent)
		} else {
			files = append(files, ent)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	jsonOK(w, map[string]any{"path": path, "parent": filepath.Dir(path), "entries": append(dirs, files...)})
}

// unescapeMount decodes the octal escapes /proc/mounts uses for spaces etc.
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
