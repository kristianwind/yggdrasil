package api

import (
	"context"
	"strings"
	"testing"
)

// Several panels commonly post into one chat channel, so a message that names
// only the server is ambiguous — and worse than ambiguous when the same site
// exists on two panels, which is how an alert about a live site got read as
// activity on a stopped copy of it elsewhere.

func TestPanelLabelPrefersTheConfiguredName(t *testing.T) {
	s := testServer(t)
	s.setSetting(context.Background(), "panel_name", "  NPanel  ")
	if got := s.panelLabel(); got != "NPanel" {
		t.Errorf("got %q, want the trimmed panel name", got)
	}
}

// A panel whose name was never set still has to identify itself, or the label
// would be worse than useless — present but empty.
func TestPanelLabelFallsBackToSomethingIdentifying(t *testing.T) {
	s := testServer(t)
	got := s.panelLabel()
	if strings.TrimSpace(got) == "" {
		t.Fatal("the label must never be blank")
	}
	// Whatever it resolved to, it must not be the empty setting leaking through.
	if got == "" || got == " " {
		t.Errorf("got %q", got)
	}
}
