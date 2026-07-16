package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// repairDataPerms fixes ownership/permissions on a server's data dir using a
// root container, then makes the given path writable. SteamCMD (and other
// root-running installs) can leave files owned by root that the panel service
// user can't overwrite from the host; this hands them back to the panel uid.
func (s *Server) repairDataPerms(ctx context.Context, serverID, relPath string) error {
	var dataDir string
	if err := s.db.QueryRowContext(ctx, "SELECT data_dir FROM servers WHERE id=?", serverID).Scan(&dataDir); err != nil {
		return err
	}
	image := "busybox:latest"
	if rt, err := s.loadRuntime(ctx, serverID); err == nil && rt.gs.Docker.Image != "" {
		image = rt.gs.Docker.Image // already pulled for this server
	}
	// Hand the whole tree back to the panel user, and ensure the specific target
	// is writable. Best-effort: ignore errors inside the container.
	//
	// SECURITY: relPath is user-controlled and this runs as root via /bin/sh -c, so
	// it MUST be shell-single-quoted. Go's %q emits double quotes, which leave $(),
	// backticks and $VAR active — that was a root command-injection vector.
	script := fmt.Sprintf(
		"chown -R %d:%d /data 2>/dev/null || true; chmod -f u+rw %s 2>/dev/null || true; chmod -f u+rwx %s 2>/dev/null || true",
		os.Getuid(), os.Getgid(),
		shellSingleQuote("/data/"+relPath), shellSingleQuote("/data/"+filepath.Dir(relPath)))
	return s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image: image, DataDir: dataDir, Script: script, User: "0:0", // root so chown works
	}, io.Discard)
}

// shellSingleQuote wraps s in single quotes safe for /bin/sh, escaping embedded
// single quotes. Inside single quotes the shell treats everything literally, so
// $(), backticks, $VAR and ; cannot inject — use this for any user-controlled
// value interpolated into a shell command string.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// safeJoin resolves rel against the server's data dir and guarantees the result
// stays inside it (defends against ../ traversal and absolute paths).
func safeJoin(dataDir, rel string) (string, bool) {
	clean := filepath.Clean("/" + strings.TrimSpace(rel)) // force absolute, strips ../
	full := filepath.Join(dataDir, clean)
	rp, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return "", false
	}
	if rp != base && !strings.HasPrefix(rp, base+string(os.PathSeparator)) {
		return "", false
	}
	// Symlink defense: a symlink *inside* the data dir could still point outside it.
	// Resolve the nearest existing ancestor (rp itself may not exist yet for a new
	// file) and re-check it stays within the resolved base.
	evalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", false
	}
	probe := rp
	for {
		if ev, e := filepath.EvalSymlinks(probe); e == nil {
			if ev != evalBase && !strings.HasPrefix(ev, evalBase+string(os.PathSeparator)) {
				return "", false
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break // reached root without an existing ancestor
		}
		probe = parent
	}
	return rp, true
}

// serverDataDir resolves the server's data directory and enforces the
// ServerFiles permission, writing the appropriate error response on failure.
func (s *Server) serverDataDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	var dataDir string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT data_dir FROM servers WHERE id=?", id).Scan(&dataDir); err != nil {
		jsonError(w, "server not found", http.StatusNotFound)
		return "", false
	}
	if !s.can(w, r, rbac.ServerFiles, s.serverTarget(r.Context(), id)) {
		return "", false
	}
	return dataDir, true
}

type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	dir, ok := safeJoin(dataDir, rel)
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fileError(w, "list", rel, err)
		return
	}
	list := []fileEntry{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, fileEntry{
			Name:  e.Name(),
			Path:  filepath.Join(rel, e.Name()),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}
	jsonOK(w, list)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	full, ok := safeJoin(dataDir, rel)
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		fileError(w, "read", rel, err)
		return
	}
	if len(data) > 5*1024*1024 {
		jsonError(w, "file too large to edit (>5MB)", http.StatusRequestEntityTooLarge)
		return
	}
	jsonOK(w, map[string]string{"content": string(data)})
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	full, ok := safeJoin(dataDir, req.Path)
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Snapshot the current contents before overwriting, so edits can be rolled back.
	s.snapshotFileVersion(chi.URLParam(r, "id"), req.Path, full)
	err := os.WriteFile(full, []byte(req.Content), 0644)
	if err != nil && errors.Is(err, os.ErrPermission) {
		// Likely a root-owned file left by a SteamCMD install. Repair ownership
		// via a root container and retry once.
		if rerr := s.repairDataPerms(r.Context(), chi.URLParam(r, "id"), req.Path); rerr == nil {
			err = os.WriteFile(full, []byte(req.Content), 0644)
		}
	}
	if err != nil {
		jsonError(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "file.write", "server:"+chi.URLParam(r, "id"), map[string]string{"path": req.Path})
	jsonOK(w, map[string]string{"status": "saved"})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	full, ok := safeJoin(dataDir, r.URL.Query().Get("path"))
	if !ok || full == dataDir {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(full); err != nil {
		jsonError(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "file.delete", "server:"+chi.URLParam(r, "id"), nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	rel := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "form file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	dest, ok := safeJoin(dataDir, filepath.Join(rel, header.Filename))
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out, err := os.Create(dest)
	if err != nil {
		jsonError(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		jsonError(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "file.upload", "server:"+chi.URLParam(r, "id"), map[string]string{"name": header.Filename})
	jsonOK(w, map[string]string{"status": "uploaded"})
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	full, ok := safeJoin(dataDir, r.URL.Query().Get("path"))
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		jsonError(w, "not a file", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(full)+"\"")
	http.ServeFile(w, r, full)
}

// fileError turns an os error into a response that says what happened without
// saying where.
//
// These handlers used to pass err.Error() straight through, and an os error
// carries the full resolved path — so a missing file answered with the panel's
// absolute layout, e.g. "/var/lib/yggdrasil/servers/<uuid>/server.properties".
// That needs only server.files, which a delegate can hold without being an admin,
// and it tells them nothing they need: they asked about a path relative to the
// server, so the answer should be too.
//
// It also fixes the status. A file that isn't there is a 404, not a 400 — the
// request was fine. The frontend can then tell "not there yet" apart from "went
// wrong" by status instead of by matching on the wording of an error string.
func fileError(w http.ResponseWriter, op, rel string, err error) {
	name := strings.TrimPrefix(rel, "/")
	if name == "" {
		name = "that path"
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		jsonError(w, name+": no such file or directory", http.StatusNotFound)
	case errors.Is(err, fs.ErrPermission):
		jsonError(w, name+": permission denied", http.StatusForbidden)
	default:
		// Anything else is ours to explain, so log it with detail and keep the
		// response generic.
		log.Printf("files: %s %q: %v", op, rel, err)
		jsonError(w, "could not "+op+" "+name, http.StatusInternalServerError)
	}
}
