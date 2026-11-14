package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/backup"
)

// The contents of a data directory belong to whoever runs code in the server's
// container — a plugin is enough. The panel reads and writes that directory as
// its own user, which can also read /etc/yggdrasil/config.yaml. So a symlink in
// there must never be a way out, for any file operation, at any depth.

func fileReq(t *testing.T, method, target, serverID, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", serverID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"})
	return r.WithContext(ctx)
}

func TestFileHandlersDoNotFollowSymlinksOutOfTheDataDir(t *testing.T) {
	s, id, dir := seedFileServer(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "config.yaml")
	if err := os.WriteFile(secret, []byte("secret_key: hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "direct")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"out/config.yaml", "direct"} {
		w := httptest.NewRecorder()
		s.handleReadFile(w, fileReq(t, "GET", "/x?path="+p, id, ""))
		if w.Code == 200 || strings.Contains(w.Body.String(), "hunter2") {
			t.Errorf("read %s: %d %s", p, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		s.handleDownloadFile(w, fileReq(t, "GET", "/x?path="+p, id, ""))
		if w.Code == 200 || strings.Contains(w.Body.String(), "hunter2") {
			t.Errorf("download %s: %d %s", p, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		s.handleWriteFile(w, fileReq(t, "PUT", "/x", id, `{"path":"`+p+`","content":"overwritten"}`))
		if w.Code == 200 {
			t.Errorf("write %s: accepted", p)
		}
	}
	w := httptest.NewRecorder()
	s.handleWriteFile(w, fileReq(t, "PUT", "/x", id, `{"path":"out/new.txt","content":"x"}`))
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Errorf("write created a file outside the data dir (%d)", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleMkdir(w, fileReq(t, "POST", "/x", id, `{"path":"out/made"}`))
	if _, err := os.Stat(filepath.Join(outside, "made")); err == nil {
		t.Errorf("mkdir created a directory outside the data dir (%d)", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleListFiles(w, fileReq(t, "GET", "/x?path=out", id, ""))
	if w.Code == 200 {
		t.Errorf("list followed the link: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleDeleteFile(w, fileReq(t, "DELETE", "/x?path=out/config.yaml", id, ""))
	if b, _ := os.ReadFile(secret); string(b) != "secret_key: hunter2" {
		t.Fatalf("the file outside the data dir was changed or deleted: %q (%d)", b, w.Code)
	}
	// Deleting the link itself is fine, and must remove the link, not the target.
	w = httptest.NewRecorder()
	s.handleDeleteFile(w, fileReq(t, "DELETE", "/x?path=out", id, ""))
	if w.Code != 200 {
		t.Errorf("deleting the symlink itself: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("deleting the link deleted its target: %v", err)
	}
}

// The control: the same handlers still do their job on ordinary paths, and a
// symlink that stays inside the data directory still works.
func TestFileHandlersStillWorkInsideTheDataDir(t *testing.T) {
	s, id, dir := seedFileServer(t)
	w := httptest.NewRecorder()
	s.handleWriteFile(w, fileReq(t, "PUT", "/x", id, `{"path":"/cfg/server.properties","content":"motd=hi"}`))
	if w.Code != 200 {
		t.Fatalf("write: %d %s", w.Code, w.Body.String())
	}
	if err := os.Symlink("cfg", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cfg/server.properties", "/cfg/server.properties", "alias/server.properties", "../cfg/server.properties"} {
		w = httptest.NewRecorder()
		s.handleReadFile(w, fileReq(t, "GET", "/x?path="+p, id, ""))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "motd=hi") {
			t.Errorf("read %s: %d %s", p, w.Code, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	s.handleListFiles(w, fileReq(t, "GET", "/x?path=", id, ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"cfg"`) {
		t.Errorf("list root: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleDownloadFile(w, fileReq(t, "GET", "/x?path=cfg/server.properties", id, ""))
	if w.Code != 200 || w.Body.String() != "motd=hi" {
		t.Errorf("download: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleDeleteFile(w, fileReq(t, "DELETE", "/x?path=", id, ""))
	if w.Code == 200 {
		t.Error("deleting the data dir itself was accepted")
	}
}

// Between two chunks the container can replace the part file with a symlink to
// any file the panel may write. The next chunk must not land there.
func TestChunkedUploadDoesNotAppendThroughASwappedPartFile(t *testing.T) {
	s, id, dir := seedFileServer(t)
	victim := filepath.Join(t.TempDir(), "start.sh")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := "feedfacefeedfacefeedfacefeedface"
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 2), []byte("first")); w.Code != 200 {
		t.Fatalf("chunk 0: %d %s", w.Code, w.Body.String())
	}
	part := filepath.Join(dir, backup.UploadScratchDir, up+".part")
	if err := os.Remove(part); err != nil {
		t.Fatalf("the part file is not where this test expects it: %v", err)
	}
	if err := os.Symlink(victim, part); err != nil {
		t.Fatal(err)
	}
	w := postUpload(t, s, id, chunkFields(up, "pack.zip", 1, 2), []byte("INJECTED"))
	if b, _ := os.ReadFile(victim); string(b) != "original\n" {
		t.Fatalf("chunk 1 was written through the symlink: %q (%d)", b, w.Code)
	}
	if w.Code == 200 {
		t.Error("the upload reported success")
	}
}

// And the scratch directory itself can be the link.
func TestChunkedUploadDoesNotCreateItsPartFileThroughASymlinkedScratchDir(t *testing.T) {
	s, id, dir := seedFileServer(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, backup.UploadScratchDir)); err != nil {
		t.Fatal(err)
	}
	up := "0000111122223333"
	w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 2), []byte("first"))
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Fatalf("a part file was created outside the data dir (%d)", w.Code)
	}
}
