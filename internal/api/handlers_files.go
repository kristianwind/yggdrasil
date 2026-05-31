package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

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

func (s *Server) serverDataDir(r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	var dataDir string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT data_dir FROM servers WHERE id=?", id).Scan(&dataDir); err != nil {
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
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
	if err := os.WriteFile(full, []byte(req.Content), 0644); err != nil {
		jsonError(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "file.write", "server:"+chi.URLParam(r, "id"), map[string]string{"path": req.Path})
	jsonOK(w, map[string]string{"status": "saved"})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
	dataDir, ok := s.serverDataDir(r)
	if !ok {
		jsonError(w, "server not found", http.StatusNotFound)
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
