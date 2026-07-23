package docskb

import (
	"strings"
	"testing"
	"testing/fstest"
)

const doc = `# Backups & schedules

Yggdrasil backs servers up to targets.

## Backup targets

Configure a target under Settings → Backups. Local disk and S3 are supported.

## Restore

Restoring overwrites the server's data — you must type the server name to confirm.
`

func testKB(t *testing.T) *KB {
	t.Helper()
	return Load(fstest.MapFS{"guides/backups.md": &fstest.MapFile{Data: []byte(doc)}})
}

func TestSplitAndRetrieve(t *testing.T) {
	kb := testKB(t)
	if len(kb.sections) != 3 { // preamble + 2 sections
		t.Fatalf("expected 3 sections, got %d", len(kb.sections))
	}
	got := kb.Retrieve("how do I restore a backup?", 3, 4000)
	if len(got) == 0 || got[0].Title != "Restore" {
		t.Fatalf("expected the Restore section first, got %+v", got)
	}
	if got[0].Page != "Backups & schedules" {
		t.Fatalf("page title lost: %+v", got[0])
	}
	// Irrelevant questions retrieve nothing — no misleading grounding.
	if got := kb.Retrieve("minecraft difficulty hardcore dragons", 3, 4000); len(got) != 0 {
		t.Fatalf("irrelevant query should return nothing, got %+v", got)
	}
	// The char budget truncates rather than drops.
	got = kb.Retrieve("backup target restore settings", 3, 120)
	total := 0
	for _, s := range got {
		total += len(s.Body)
	}
	if total > 200 {
		t.Fatalf("char budget ignored: %d chars", total)
	}
}

func TestTokensStopwords(t *testing.T) {
	ts := tokens("How can I set up the backup schedule?")
	joined := strings.Join(ts, " ")
	if strings.Contains(joined, "how") || strings.Contains(joined, "the") {
		t.Fatalf("stopwords survived: %v", ts)
	}
	if !strings.Contains(joined, "backup") || !strings.Contains(joined, "schedule") {
		t.Fatalf("content words lost: %v", ts)
	}
}
