package api

import (
	"context"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/backup"
)

// fakeTarget records deletions instead of touching a real backend.
type fakeTarget struct{ deleted []string }

func (f *fakeTarget) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	return 0, nil
}
func (f *fakeTarget) Get(ctx context.Context, name string) (io.ReadCloser, error) { return nil, nil }
func (f *fakeTarget) List(ctx context.Context) ([]backup.Object, error)           { return nil, nil }
func (f *fakeTarget) Delete(ctx context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *fakeTarget) Close() error { return nil }

// TestApplyRetentionKeepsFreshBackups drives applyRetention against a real
// database so the created_at value is whatever the schema default actually
// writes. The unit tests in internal/backup feed Retention() hand-built
// time.Time values, which is why they never caught this: the bug lives in the
// gap between what SQLite stores and what the handler parses.
//
// A keep-days policy must not delete a backup created moments ago.
func TestApplyRetentionKeepsFreshBackups(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	serverID := uuid.New().String()
	targetID := uuid.New().String()

	// keep_days=30, keep_n=0 — "keep the last month", the setting the docs suggest.
	s.db.Exec("INSERT INTO backup_targets (id, name, type, config_enc, keep_n, keep_days) VALUES (?,?,?,?,0,30)",
		targetID, "test", "local", "{}")

	// Three completed backups, all created now, all dated by the schema default.
	var ids []string
	for i := 0; i < 3; i++ {
		id := uuid.New().String()
		ids = append(ids, id)
		if _, err := s.db.Exec(
			"INSERT INTO backups (id, server_id, target_id, path, status) VALUES (?,?,?,?,'done')",
			id, serverID, targetID, "backup-"+id+".tar.gz"); err != nil {
			t.Fatalf("insert backup: %v", err)
		}
	}

	// Sanity: confirm the default really is the non-RFC3339 SQLite format, so
	// this test fails loudly if the schema changes rather than passing vacuously.
	var created string
	s.db.QueryRow("SELECT created_at FROM backups WHERE id=?", ids[0]).Scan(&created)
	if _, err := parseDBTime(created); err != nil {
		t.Fatalf("schema default %q is unparseable: %v", created, err)
	}

	ft := &fakeTarget{}
	s.applyRetention(ctx, serverID, targetID, ft)

	if len(ft.deleted) != 0 {
		t.Errorf("keep_days=30 deleted %d backups created seconds ago: %v", len(ft.deleted), ft.deleted)
	}

	var remaining int
	s.db.QueryRow("SELECT COUNT(*) FROM backups WHERE server_id=?", serverID).Scan(&remaining)
	if remaining != 3 {
		t.Errorf("expected all 3 backups to survive a 30-day policy, %d remain", remaining)
	}
}
