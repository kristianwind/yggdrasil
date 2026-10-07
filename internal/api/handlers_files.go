package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/backup"
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
	rel := filePathParam(r)
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

// filePathParam reads the file path a request is about, preferring the
// base64url-encoded form.
//
// A plain ?path=wp-config.php is a WordPress attack signature, and a web
// application firewall in front of the panel blocks the request before it ever
// arrives — the panel answers 403 for a file it can read perfectly well.
// Observed on this project: Cloudflare refused
// /api/servers/<id>/files/content?path=wp-config.php while the same request for
// index.php went through, so the file browser could not open exactly the files
// an operator most often needs. Encoding the path removes the signature without
// hiding anything: it is the same value, and safeJoin still bounds it.
//
// ?path= is still accepted so older clients, scripts and bookmarks keep working.
func filePathParam(r *http.Request) string {
	if enc := r.URL.Query().Get("path_b64"); enc != "" {
		if dec, err := base64.RawURLEncoding.DecodeString(enc); err == nil {
			return string(dec)
		}
		// Tolerate padded input — some clients will not strip it.
		if dec, err := base64.URLEncoding.DecodeString(enc); err == nil {
			return string(dec)
		}
	}
	return r.URL.Query().Get("path")
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	rel := filePathParam(r)
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

// handleMkdir creates a directory (and any missing parents) inside the server's
// data dir. Jailed by safeJoin like every other file op, and gated on the same
// ServerFiles permission via serverDataDir.
func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		jsonError(w, "path required", http.StatusBadRequest)
		return
	}
	full, ok := safeJoin(dataDir, req.Path)
	if !ok || full == dataDir {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(full, 0755); err != nil {
		jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "file.mkdir", "server:"+chi.URLParam(r, "id"), map[string]string{"path": req.Path})
	jsonOK(w, map[string]string{"status": "created"})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	full, ok := safeJoin(dataDir, filePathParam(r))
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

// Where a chunked upload accumulates: inside the server's own data directory
// rather than /tmp, because a modpack or a world is hundreds of megabytes, and
// assembling it on the same filesystem as its destination makes the final move a
// rename instead of a second full copy — and makes the space it needs the space
// the admin can already see. The name is defined once, in the backup package,
// which is the other half that has to agree about it.
const uploadDir = backup.UploadScratchDir

// uploadState tracks one in-flight chunked upload. Chunks have to arrive in
// order and exactly once: a retried chunk that simply appended again would
// produce a file that is the right name, the right place, and quietly corrupt —
// so the server decides which index it will accept next, rather than trusting
// the number the client puts in the form.
type uploadState struct {
	next    int
	tmp     string
	started time.Time
}

type uploadTracker struct {
	mu sync.Mutex
	m  map[string]*uploadState
}

func (t *uploadTracker) get(id string) (*uploadState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.m[id]
	return st, ok
}

func (t *uploadTracker) put(id string, st *uploadState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*uploadState{}
	}
	t.m[id] = st
}

func (t *uploadTracker) drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// sweep forgets uploads nobody finished and deletes what they left behind. A
// browser tab closed halfway through is the ordinary case, not an error, and
// without this every one of them keeps its partial file on the admin's disk for
// good — in a folder they have no reason to look in.
func (t *uploadTracker) sweep(olderThan time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, st := range t.m {
		if time.Since(st.started) > olderThan {
			os.Remove(st.tmp) //nolint:errcheck
			delete(t.m, id)
		}
	}
}

// uploadIDRe keeps an upload id to characters that cannot climb out of the
// upload directory or name something else. It is part of a filename.
var uploadIDRe = regexp.MustCompile(`^[a-zA-Z0-9]{8,64}$`)

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	// 8 MB of form in memory; anything larger spills to a temp file. A chunk is
	// well under this, and a whole-file upload still works the way it always did.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
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

	// The browser sends its own name for a chunk; the real one is a separate
	// field, because a chunk's Blob has no filename of its own.
	name := header.Filename
	if n := strings.TrimSpace(r.FormValue("name")); n != "" {
		name = n
	}
	dest, ok := safeJoin(dataDir, filepath.Join(rel, filepath.Base(name)))
	if !ok {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	count, _ := strconv.Atoi(r.FormValue("chunk_count"))
	if count <= 1 {
		// Whole file in one request — unchanged, and still what a small file and
		// any existing API client does.
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
		s.auditLog(r, "file.upload", "server:"+chi.URLParam(r, "id"), map[string]string{"name": name})
		jsonOK(w, map[string]string{"status": "uploaded"})
		return
	}

	id := r.FormValue("upload_id")
	if !uploadIDRe.MatchString(id) {
		jsonError(w, "invalid upload id", http.StatusBadRequest)
		return
	}
	index, err := strconv.Atoi(r.FormValue("chunk_index"))
	if err != nil || index < 0 || index >= count {
		jsonError(w, "invalid chunk index", http.StatusBadRequest)
		return
	}
	s.uploads.sweep(6 * time.Hour)

	st, known := s.uploads.get(id)
	if index == 0 {
		if err := os.MkdirAll(filepath.Join(dataDir, uploadDir), 0755); err != nil {
			jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		st = &uploadState{tmp: filepath.Join(dataDir, uploadDir, id+".part"), started: time.Now()}
		os.Remove(st.tmp) //nolint:errcheck
		s.uploads.put(id, st)
	} else if !known {
		// The panel restarted, or the upload was swept. Say which, because the
		// browser's only other reading of a 409 is "this file already exists".
		jsonError(w, "this upload is no longer in progress — start it again", http.StatusConflict)
		return
	}
	if index != st.next {
		jsonError(w, fmt.Sprintf("expected chunk %d, got %d", st.next, index), http.StatusConflict)
		return
	}

	out, err := os.OpenFile(st.tmp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		jsonError(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		jsonError(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := out.Close(); err != nil {
		jsonError(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	st.next++

	if st.next < count {
		jsonOK(w, map[string]any{"status": "chunk", "received": st.next, "of": count})
		return
	}
	// Last chunk: the assembled file becomes the real one in a single rename, so
	// nothing ever observes it half-written under its final name.
	if err := os.Rename(st.tmp, dest); err != nil {
		os.Remove(st.tmp) //nolint:errcheck
		s.uploads.drop(id)
		jsonError(w, "finish: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.uploads.drop(id)
	s.auditLog(r, "file.upload", "server:"+chi.URLParam(r, "id"), map[string]string{"name": name, "chunks": strconv.Itoa(count)})
	jsonOK(w, map[string]string{"status": "uploaded"})
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	full, ok := safeJoin(dataDir, filePathParam(r))
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
