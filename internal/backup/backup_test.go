package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRestoreRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "server.properties"), "motd=hi")
	writeFile(t, filepath.Join(src, "world", "level.dat"), "WORLD")
	writeFile(t, filepath.Join(src, "logs", "latest.log"), "ignore me")

	// Only back up world + server.properties (simulating backup.include).
	var buf bytes.Buffer
	if err := Archive(src, []string{"world", "server.properties"}, &buf); err != nil {
		t.Fatalf("archive: %v", err)
	}

	dest := t.TempDir()
	if err := Restore(&buf, dest); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if got, _ := os.ReadFile(filepath.Join(dest, "world", "level.dat")); string(got) != "WORLD" {
		t.Errorf("world/level.dat = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "server.properties")); string(got) != "motd=hi" {
		t.Errorf("server.properties = %q", got)
	}
	// logs/ was excluded.
	if _, err := os.Stat(filepath.Join(dest, "logs")); !os.IsNotExist(err) {
		t.Error("logs/ should not have been archived")
	}
}

func TestArchiveWholeDirWhenNoInclude(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "A")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "B")

	var buf bytes.Buffer
	if err := Archive(src, nil, &buf); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Restore(&buf, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "sub", "b.txt")); string(got) != "B" {
		t.Errorf("sub/b.txt = %q", got)
	}
}

func TestLocalTargetPutListGetDelete(t *testing.T) {
	dir := t.TempDir()
	tgt, err := Open(Config{Type: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer tgt.Close()
	ctx := context.Background()

	n, err := tgt.Put(ctx, "backup-1.tar.gz", bytes.NewReader([]byte("payload")))
	if err != nil || n != 7 {
		t.Fatalf("put: n=%d err=%v", n, err)
	}
	objs, err := tgt.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != "backup-1.tar.gz" {
		t.Fatalf("list: %+v err=%v", objs, err)
	}
	rc, err := tgt.Get(ctx, "backup-1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "backup-1.tar.gz"))
	rc.Close()
	if string(data) != "payload" {
		t.Errorf("get content = %q", data)
	}
	if err := tgt.Delete(ctx, "backup-1.tar.gz"); err != nil {
		t.Fatal(err)
	}
	objs, _ = tgt.List(ctx)
	if len(objs) != 0 {
		t.Errorf("expected empty after delete, got %d", len(objs))
	}
}

func TestRetention(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	mk := func(daysAgo int) Object {
		return Object{Name: "b", ModTime: now.AddDate(0, 0, -daysAgo)}
	}
	objs := []Object{mk(0), mk(1), mk(5), mk(10), mk(40)}

	// keep newest 2 → delete the 3 older.
	del := Retention(objs, 2, 0, now)
	if len(del) != 3 {
		t.Errorf("keepN=2: expected 3 deletions, got %d", len(del))
	}

	// keep 7 days → delete the 10- and 40-day-old ones.
	del = Retention(objs, 0, 7, now)
	if len(del) != 2 {
		t.Errorf("keepDays=7: expected 2 deletions, got %d", len(del))
	}

	// no policy → keep all.
	if del = Retention(objs, 0, 0, now); len(del) != 0 {
		t.Errorf("no policy: expected 0 deletions, got %d", len(del))
	}

	// combined: keep newest 1 OR within 7 days → newest1=day0; within7=day0,1,5;
	// union keeps {0,1,5}; deletes {10,40}.
	del = Retention(objs, 1, 7, now)
	if len(del) != 2 {
		t.Errorf("combined: expected 2 deletions, got %d", len(del))
	}
}

func TestVerify(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "server.properties"), "motd=hi")
	writeFile(t, filepath.Join(src, "world", "level.dat"), "WORLD-DATA")

	var buf bytes.Buffer
	if err := Archive(src, nil, &buf); err != nil {
		t.Fatalf("archive: %v", err)
	}
	good := buf.Bytes()

	// A valid archive verifies: 2 files, total bytes = the two file bodies.
	entries, total, err := Verify(bytes.NewReader(good))
	if err != nil {
		t.Fatalf("verify good: %v", err)
	}
	if entries != 2 || total != int64(len("motd=hi")+len("WORLD-DATA")) {
		t.Fatalf("verify good: entries=%d total=%d, want 2/%d", entries, total, len("motd=hi")+len("WORLD-DATA"))
	}

	// A truncated archive fails (chopped mid-stream → bad gzip CRC / short read).
	if _, _, err := Verify(bytes.NewReader(good[:len(good)-20])); err == nil {
		t.Fatal("verify truncated: expected an error, got nil")
	}

	// Not a gzip at all → clear error.
	if _, _, err := Verify(bytes.NewReader([]byte("this is not a gzip archive"))); err == nil {
		t.Fatal("verify garbage: expected an error, got nil")
	}
}
