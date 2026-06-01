package api

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	script := fmt.Sprintf(
		"chown -R %d:%d /data 2>/dev/null || true; chmod -f u+rw %q 2>/dev/null || true; chmod -f u+rwx %q 2>/dev/null || true",
		os.Getuid(), os.Getgid(),
		"/data/"+relPath, "/data/"+filepath.Dir(relPath))
	return s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image: image, DataDir: dataDir, Script: script, User: "0:0", // root so chown works
	}, io.Discard)
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
		jsonError(w, "read dir: "+err.Error(), http.StatusBadRequest)
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
	full, ok := safeJoin(dataDir, r.URL.Query().Get("path"))
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		jsonError(w, "read file: "+err.Error(), http.StatusBadRequest)
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
