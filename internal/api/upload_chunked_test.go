package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/backup"
)

// A panel reached over a Cloudflare tunnel has its request body capped at 100 MB,
// and an nginx in front of one has a limit of its own. A modpack, a world or a
// site archive is bigger than that, and for a user whose only access to the
// server is the Files tab, a whole-file POST is not slow — it is no way in.

func seedFileServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := testServer(t)
	id := uuid.New().String()
	dir := t.TempDir()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'stopped')`,
		id, "upload-test", "minecraft-java", dir); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return s, id, dir
}

// postUpload drives the real handler. fields beyond the file are optional, so the
// same helper exercises both the chunked and the whole-file path.
func postUpload(t *testing.T, s *Server, serverID string, fields map[string]string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if k == "filename" {
			continue
		}
		mw.WriteField(k, v) //nolint:errcheck
	}
	fw, err := mw.CreateFormFile("file", fields["filename"])
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data) //nolint:errcheck
	mw.Close()     //nolint:errcheck

	r := httptest.NewRequest("POST", "/api/servers/"+serverID+"/files/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", serverID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"})
	w := httptest.NewRecorder()
	s.handleUploadFile(w, r.WithContext(ctx))
	return w
}

func chunkFields(id, name string, index, count int) map[string]string {
	return map[string]string{
		"path": "", "filename": "blob", "name": name,
		"upload_id": id, "chunk_index": strconv.Itoa(index), "chunk_count": strconv.Itoa(count),
	}
}

func TestChunkedUploadAssemblesTheWholeFile(t *testing.T) {
	s, id, dir := seedFileServer(t)
	payload := make([]byte, 300_000)
	rand.Read(payload) //nolint:errcheck

	const chunks = 5
	size := len(payload) / chunks
	up := "0123456789abcdef0123456789abcdef"
	for i := 0; i < chunks; i++ {
		w := postUpload(t, s, id, chunkFields(up, "pack.zip", i, chunks), payload[i*size:(i+1)*size])
		if w.Code != 200 {
			t.Fatalf("chunk %d: %d %s", i, w.Code, w.Body.String())
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "pack.zip"))
	if err != nil {
		t.Fatalf("assembled file missing: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("assembled file is %d bytes and differs from the %d uploaded", len(got), len(payload))
	}
	// The scratch file must not survive its own upload, or every upload leaves a
	// second copy of itself on the admin's disk.
	left, _ := filepath.Glob(filepath.Join(dir, backup.UploadScratchDir, "*"))
	if len(left) != 0 {
		t.Errorf("scratch left behind after a finished upload: %v", left)
	}
}

// The dangerous failure is not a lost chunk, it is a repeated one: appending the
// same piece twice produces a file with the right name, in the right place, and
// quietly corrupt. So a repeated chunk past the first is refused.
//
// Chunk 0 is deliberately the exception, and the two halves belong in one test
// because the second is only safe while the first holds. The client restarts an
// upload from 0 with smaller pieces when something in the path answers 413, so
// index 0 has to mean "begin again" — it truncates rather than appends, and the
// file that comes out is still exactly what was sent.
func TestChunkedUploadRefusesARepeatedChunkButRestartsFromTheFirst(t *testing.T) {
	s, id, dir := seedFileServer(t)
	up := "aaaaaaaabbbbbbbbccccccccdddddddd"
	a, b := []byte("first-half-"), []byte("second-half")

	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 2), a); w.Code != 200 {
		t.Fatalf("chunk 0: %d %s", w.Code, w.Body.String())
	}
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 2), a); w.Code != 200 {
		t.Fatalf("a restarted chunk 0 returned %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 1, 2), b); w.Code != 200 {
		t.Fatalf("chunk 1: %d %s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "pack.zip"))
	if err != nil {
		t.Fatal(err)
	}
	// Not "it is two chunks long" — the actual bytes. A double append produces a
	// file of exactly the right shape and the wrong content.
	if string(got) != string(a)+string(b) {
		t.Errorf("file = %q, want %q — chunk 0 was appended rather than restarted", got, string(a)+string(b))
	}

	// And now the half that must not be forgiving.
	up2 := "2222222233333333444444445555555"
	if w := postUpload(t, s, id, chunkFields(up2, "two.zip", 0, 3), a); w.Code != 200 {
		t.Fatalf("chunk 0: %d", w.Code)
	}
	if w := postUpload(t, s, id, chunkFields(up2, "two.zip", 1, 3), b); w.Code != 200 {
		t.Fatalf("chunk 1: %d", w.Code)
	}
	if w := postUpload(t, s, id, chunkFields(up2, "two.zip", 1, 3), b); w.Code != 409 {
		t.Errorf("a repeated chunk 1 returned %d, want 409 — it would be appended twice", w.Code)
	}
}

func TestChunkedUploadRefusesAChunkOutOfOrder(t *testing.T) {
	s, id, dir := seedFileServer(t)
	up := "eeeeeeeeffffffff00000000gggggggg"
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 3), []byte("one")); w.Code != 200 {
		t.Fatalf("chunk 0: %d", w.Code)
	}
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 2, 3), []byte("three")); w.Code != 409 {
		t.Errorf("chunk 2 before chunk 1 returned %d, want 409", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "pack.zip")); err == nil {
		t.Error("an unfinished upload produced the destination file")
	}
}

// An upload the server has no record of — the panel restarted, or the sweep
// collected it — must not be silently resumed from whatever is on disk.
func TestChunkedUploadRefusesAnUnknownUpload(t *testing.T) {
	s, id, _ := seedFileServer(t)
	w := postUpload(t, s, id, chunkFields("hhhhhhhhiiiiiiiijjjjjjjjkkkkkkkk", "pack.zip", 1, 2), []byte("x"))
	if w.Code != 409 {
		t.Errorf("chunk 1 of an unknown upload returned %d, want 409", w.Code)
	}
}

func TestChunkedUploadRejectsABadUploadID(t *testing.T) {
	s, id, dir := seedFileServer(t)
	f := chunkFields("../../../etc/cron.d/x", "pack.zip", 0, 2)
	if w := postUpload(t, s, id, f, []byte("x")); w.Code != 400 {
		t.Errorf("an upload id with a path in it returned %d, want 400", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, backup.UploadScratchDir)); err == nil {
		t.Error("a rejected upload id still created the scratch directory")
	}
}

// The name field is the one a chunk carries its real filename in, so it is also
// the one somebody would try to climb out of the data directory with.
func TestChunkedUploadCannotWriteOutsideTheDataDir(t *testing.T) {
	s, id, dir := seedFileServer(t)
	outside := filepath.Join(filepath.Dir(dir), "escaped.txt")
	up := "llllllllmmmmmmmmnnnnnnnnoooooooo"

	f := chunkFields(up, "../escaped.txt", 0, 1)
	f["chunk_count"] = "2"
	postUpload(t, s, id, f, []byte("x")) //nolint:errcheck
	f2 := chunkFields(up, "../escaped.txt", 1, 2)
	postUpload(t, s, id, f2, []byte("y")) //nolint:errcheck

	if _, err := os.Stat(outside); err == nil {
		t.Errorf("a chunked upload wrote outside the server's data directory: %s", outside)
	}
	// The path field is the other half of the destination, and safeJoin CLAMPS it
	// rather than refusing it: "/"+rel is cleaned to an absolute path first, so
	// "../.." becomes "/" and lands at the top of the data directory. Measured,
	// because the status code reads like a bug and is not one — what matters is
	// where the bytes went, so that is what this asserts.
	esc := map[string]string{"path": "../..", "filename": "escaped2.txt"}
	if w := postUpload(t, s, id, esc, []byte("z")); w.Code != 200 {
		t.Errorf("path '../..' returned %d, want it clamped and accepted", w.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped2.txt")); err == nil {
		t.Error("the path field escaped the data directory")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "escaped2.txt")); err != nil || string(b) != "z" {
		t.Errorf("clamped upload did not land at the data directory root: %q err %v", b, err)
	}
}

// Whole-file uploads are what every existing client and every small file does.
func TestWholeFileUploadStillWorks(t *testing.T) {
	s, id, dir := seedFileServer(t)
	w := postUpload(t, s, id, map[string]string{"path": "sub", "filename": "note.txt"}, []byte("hello"))
	if w.Code != 200 {
		t.Fatalf("upload returned %d: %s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "note.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("file = %q, err %v", got, err)
	}
}

// A browser tab closed halfway through is the ordinary case, not an error. What
// it leaves behind is up to a whole modpack of scratch, in a folder the admin has
// no reason to look in.
func TestAbandonedUploadIsSwept(t *testing.T) {
	s, id, dir := seedFileServer(t)
	up := "pppppppprrrrrrrrssssssssstttttttt"
	if w := postUpload(t, s, id, chunkFields(up, "pack.zip", 0, 9), []byte("partial")); w.Code != 200 {
		t.Fatalf("chunk 0: %d %s", w.Code, w.Body.String())
	}
	part := filepath.Join(dir, backup.UploadScratchDir, up+".part")
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("scratch file not written: %v", err)
	}
	s.uploads.sweep(0)
	if _, err := os.Stat(part); err == nil {
		t.Error("the sweep left the abandoned scratch file on disk")
	}
	if _, ok := s.uploads.get(up); ok {
		t.Error("the sweep left the upload in the tracker")
	}
}
