package api

import (
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// 3dekoration.dk, 2026-10-08: a WordPress server created before the rune had an
// object cache. The app update stopped the site, waited sixty seconds for a
// redis container that was never going to exist, and failed with "the app's
// services did not come up" — which is true, and names the wrong service.
//
// OBJECT_CACHE is not in that server's env at all, so the template resolves to
// nothing and the sidecar is off. startStack reads that correctly and skips it;
// everything that WAITS for the stack read the rune's full service list instead.
func wpStack() *gameskill.Gameskill {
	return &gameskill.Gameskill{
		Services: []gameskill.Service{
			{Name: "db", Image: "mariadb:lts"},
			{Name: "redis", Image: "redis:7", Enabled: "{{OBJECT_CACHE}}"},
		},
	}
}

func names(svcs []gameskill.Service) []string {
	out := []string{}
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return out
}

func eq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEnabledSidecarsSkipsAnOptionalOneThatIsOff(t *testing.T) {
	gs := wpStack()
	for _, c := range []struct {
		why  string
		env  map[string]string
		want []string
	}{
		{"the variable does not exist on this server at all (the real case)", map[string]string{}, []string{"db"}},
		{"explicitly off", map[string]string{"OBJECT_CACHE": "false"}, []string{"db"}},
		{"empty value", map[string]string{"OBJECT_CACHE": ""}, []string{"db"}},
		{"on", map[string]string{"OBJECT_CACHE": "true"}, []string{"db", "redis"}},
		{"on, written as 1", map[string]string{"OBJECT_CACHE": "1"}, []string{"db", "redis"}},
	} {
		got := names(enabledSidecars(gs, c.env))
		if !eq(got, c.want...) {
			t.Errorf("%s: enabledSidecars = %v, want %v", c.why, got, c.want)
		}
	}
}

// A service with no Enabled at all is every sidecar written before the field
// existed. It must stay required, or an app update would skip waiting for the
// database and run its migrations against nothing.
func TestEnabledSidecarsKeepsUnconditionalServices(t *testing.T) {
	gs := &gameskill.Gameskill{Services: []gameskill.Service{
		{Name: "db"}, {Name: "cache"}, {Name: "worker"},
	}}
	if got := names(enabledSidecars(gs, map[string]string{})); !eq(got, "db", "cache", "worker") {
		t.Errorf("enabledSidecars = %v, want all three", got)
	}
}

// A value that is neither true nor false counts as REQUIRED, which is the
// opposite of how startStack reads it — and that is the point. startStack turns
// it into an error and refuses to bring the stack up. If this said "off", the
// caller would decide the stack was fine, never call startStack, and never see
// that error; the update would then run against a database that is not there.
func TestEnabledSidecarsTreatsAnUnreadableValueAsRequired(t *testing.T) {
	gs := wpStack()
	got := names(enabledSidecars(gs, map[string]string{"OBJECT_CACHE": "maybe"}))
	if !eq(got, "db", "redis") {
		t.Errorf("enabledSidecars = %v, want both so startStack runs and reports the bad value", got)
	}
	// And confirm the premise rather than assuming it: IsEnabled really does
	// reject that value, which is what makes counting it in the safe direction.
	if _, err := gs.Services[1].IsEnabled(map[string]string{"OBJECT_CACHE": "maybe"}); err == nil {
		t.Error("IsEnabled accepted \"maybe\"; this test's reasoning no longer holds")
	}
}
