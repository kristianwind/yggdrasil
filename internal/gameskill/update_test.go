package gameskill

import (
	"strings"
	"testing"
)

// The update block is what lets a rune patch an app that is ALREADY installed —
// the path install cannot take, because an app image only populates an empty data
// dir. Parse it, and refuse the one shape that would silently do nothing: an
// update with no script.
func TestUpdateParsing(t *testing.T) {
	ok := importBase + `
  update:
    image: "wordpress:cli"
    label: "Update WordPress"
    script: |
      wp core update --allow-root
`
	gs, err := Parse([]byte(ok))
	if err != nil {
		t.Fatalf("valid update rejected: %v", err)
	}
	if gs.Update == nil || gs.Update.Image != "wordpress:cli" || gs.Update.Label != "Update WordPress" {
		t.Fatalf("update not parsed: %+v", gs.Update)
	}
	if !strings.Contains(gs.Update.Script, "wp core update") {
		t.Errorf("update script not parsed: %q", gs.Update.Script)
	}

	// image is optional — it falls back to the app's own image.
	minimal, err := Parse([]byte(importBase + "\n  update:\n    script: \"echo hi\"\n"))
	if err != nil {
		t.Fatalf("update without image rejected: %v", err)
	}
	if minimal.Update == nil || minimal.Update.Image != "" {
		t.Fatalf("expected empty image, got %+v", minimal.Update)
	}

	if _, err := Parse([]byte(importBase + "\n  update:\n    image: \"alpine\"\n")); err == nil {
		t.Error("update with no script should be rejected")
	}
}
