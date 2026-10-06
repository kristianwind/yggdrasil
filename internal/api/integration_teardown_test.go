package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The ordering is the whole fix, so the ordering is what is tested.
//
// Every per-server removal path begins by checking its own enable flag and
// returning when it is off. That is sensible in isolation and it means the
// moment the setting is cleared, the panel can no longer undo its own work.
// Enable UPnP, start three servers, disable UPnP, stop them: the three mappings
// stay on the router with a permanent lease, while the settings page says "off".
func TestDisablingAnIntegrationClosesWhatItOpenedFirst(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	var ids []string
	for i := 0; i < 3; i++ {
		id := uuid.New().String()
		if _, err := s.db.Exec(
			`INSERT INTO servers (id, name, gameskill_id, status, data_dir, installed, install_status)
			 VALUES (?,?,?,'running','/tmp/x',1,'done')`, id, "s"+id[:4], "minecraft-java"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		ids = append(ids, id)
	}

	s.setSetting(ctx, "upnp_enabled", "1")

	var closed []string
	remove := func(id string) {
		// The real remove paths read the flag and bail when it is off. Assert the
		// state they would see, rather than trusting the order by reading the code.
		if s.getSetting(context.Background(), "upnp_enabled") != "1" {
			t.Errorf("teardown ran for %s AFTER the setting was cleared — "+
				"the real removal path would have returned immediately and left the mapping open", id)
		}
		closed = append(closed, id)
	}

	s.closeWhatIntegrationOpened(ctx, "upnp", true, false, remove)
	s.setSetting(ctx, "upnp_enabled", "0")

	if len(closed) != len(ids) {
		t.Errorf("closed %d of %d servers", len(closed), len(ids))
	}
}

// Only the on-to-off transition tears anything down. Saving the settings form
// with the integration still on, or still off, must not go round removing
// everything it has just set up.
func TestTeardownOnlyRunsOnTheTransitionOff(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, data_dir, installed, install_status)
		 VALUES (?,?,?,'running','/tmp/x',1,'done')`, id, "only", "minecraft-java"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct {
		name     string
		was, now bool
		want     int
	}{
		{"on stays on — a settings save must not tear down a working setup", true, true, 0},
		{"off stays off", false, false, 0},
		{"off turns on", false, true, 0},
		{"on turns off", true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			s.closeWhatIntegrationOpened(ctx, "upnp", tc.was, tc.now, func(string) { n++ })
			if n != tc.want {
				t.Errorf("removed %d, want %d", n, tc.want)
			}
		})
	}
}

// The helper is correct in isolation; the ordering that matters is at the four
// call sites, and that cannot be driven end to end without a real router, proxy
// and Cloudflare account. So it is read out of the source.
//
// A structural check, with the limits that implies: it proves the teardown is
// written before the save, not that it ran. It is here because the alternative
// is no check at all on the one property the fix consists of.
func TestEveryIntegrationTearsDownBeforeItSaves(t *testing.T) {
	for _, tc := range []struct{ file, setting string }{
		{"handlers_settings.go", "upnp_enabled"},
		{"handlers_unifi.go", "unifi_enabled"},
		{"handlers_npm.go", "npm_enabled"},
		{"handlers_cloudflare.go", "cf_enabled"},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			b, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			src := string(b)

			save := strings.Index(src, `setSetting(r.Context(), "`+tc.setting+`"`)
			if save < 0 {
				t.Fatalf("%s no longer saves %s — this check lost its subject", tc.file, tc.setting)
			}
			down := strings.Index(src, "closeWhatIntegrationOpened")
			if down < 0 {
				t.Fatalf("%s saves %s and never closes what it opened: disabling it leaves "+
					"the router mappings, proxy hosts or DNS records in place", tc.file, tc.setting)
			}
			if down > save {
				t.Errorf("%s clears %s before tearing down. Every removal path checks that flag "+
					"and returns when it is off, so the teardown is a no-op in that order — "+
					"which is the bug, restored", tc.file, tc.setting)
			}
		})
	}
}
