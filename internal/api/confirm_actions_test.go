package api

import (
	"context"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/db"
)

// The default has to be ON for an install that has never touched the setting —
// an unset value must not read as "off", or every existing panel would silently
// keep the old behaviour and the feature would appear not to work.
func TestConfirmActionsDefaultsOn(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	s := &Server{db: d}
	ctx := context.Background()

	if !s.confirmActionsEnabled(ctx) {
		t.Error("unset confirm_actions should read as enabled")
	}
	s.setSetting(ctx, "confirm_actions", "0")
	if s.confirmActionsEnabled(ctx) {
		t.Error(`confirm_actions="0" should read as disabled`)
	}
	s.setSetting(ctx, "confirm_actions", "1")
	if !s.confirmActionsEnabled(ctx) {
		t.Error(`confirm_actions="1" should read as enabled`)
	}
}
