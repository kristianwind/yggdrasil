package api

import (
	"net/http"
	"os"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Config-file version history. The file editor snapshots a text file's previous
// contents before overwriting it, so a change that breaks a server can be rolled
// back. Snapshots are capped per file and skip binaries / large files.

const (
	fileVersionMaxBytes = 256 * 1024 // don't snapshot files larger than this
	fileVersionKeep     = 10         // versions retained per file
)

// snapshotFileVersion records the current contents of full (a text file within the
// server's data dir) before it's overwritten. Best-effort: silently skips missing,
// directory, binary, or oversized files.
func (s *Server) snapshotFileVersion(serverID, relPath, full string) {
	info, err := os.Stat(full)
	if err != nil || info.IsDir() || info.Size() > fileVersionMaxBytes {
		return
	}
	old, err := os.ReadFile(full)
	if err != nil || !utf8.Valid(old) {
		return
	}
	s.db.Exec("INSERT INTO file_versions (id, server_id, path, content, size) VALUES (?,?,?,?,?)",
		uuid.New().String(), serverID, relPath, string(old), len(old))
	s.db.Exec(`DELETE FROM file_versions WHERE server_id=? AND path=? AND id NOT IN (
		SELECT id FROM file_versions WHERE server_id=? AND path=? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		serverID, relPath, serverID, relPath, fileVersionKeep)
}

// handleListFileVersions lists the saved snapshots for a path (metadata only).
func (s *Server) handleListFileVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.serverDataDir(w, r); !ok { // gates on ServerFiles
		return
	}
	id := chi.URLParam(r, "id")
	path := r.URL.Query().Get("path")
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, size, created_at FROM file_versions WHERE server_id=? AND path=? ORDER BY created_at DESC, rowid DESC",
		id, path)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type ver struct {
		ID        string `json:"id"`
		Size      int    `json:"size"`
		CreatedAt string `json:"created_at"`
	}
	list := []ver{}
	for rows.Next() {
		var v ver
		if rows.Scan(&v.ID, &v.Size, &v.CreatedAt) == nil {
			list = append(list, v)
		}
	}
	jsonOK(w, list)
}

// handleGetFileVersion returns a snapshot's full contents (for preview / restore).
func (s *Server) handleGetFileVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.serverDataDir(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	vid := chi.URLParam(r, "vid")
	var content, createdAt, path string
	err := s.db.QueryRowContext(r.Context(),
		"SELECT content, created_at, path FROM file_versions WHERE id=? AND server_id=?", vid, id).
		Scan(&content, &createdAt, &path)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]string{"content": content, "created_at": createdAt, "path": path})
}
