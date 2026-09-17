package api

import "testing"

// The four names left routed-but-record-less on 2026-09-16 are the reason this
// is not a one-line "is it in the ingress list" check. Treating a surviving
// route as proof of provisioning would skip exactly the hostnames that were
// broken in the way hardest to notice: the tunnel still answers for them, and
// the world cannot resolve them.
func TestRouteWithoutDNSStillNeedsWork(t *testing.T) {
	got := cfNeedsProvision([]cfHostnameState{
		{Domain: "karakeep.example.com", HasRoute: true, HasRecord: false},
	})
	if len(got) != 1 {
		t.Fatalf("a hostname with a route but no DNS record must be restored, got %v", got)
	}
}

func TestNothingIsWrittenWhenEverythingIsPresent(t *testing.T) {
	got := cfNeedsProvision([]cfHostnameState{
		{Domain: "a.example.com", HasRoute: true, HasRecord: true},
		{Domain: "b.example.com", HasRoute: true, HasRecord: true},
	})
	if len(got) != 0 {
		t.Errorf("a healthy fleet must cost no writes at all, got %v", got)
	}
}

func TestBothHalvesMissingIsRestored(t *testing.T) {
	got := cfNeedsProvision([]cfHostnameState{
		{Domain: "verdande.example.com", HasRoute: false, HasRecord: false},
	})
	if len(got) != 1 || got[0] != "verdande.example.com" {
		t.Errorf("got %v", got)
	}
}

// A server with no hostname configured is not a server with a missing one.
func TestNoHostnameIsNotAMissingHostname(t *testing.T) {
	if got := cfNeedsProvision([]cfHostnameState{{Domain: ""}}); len(got) != 0 {
		t.Errorf("a server that claims no hostname must be left alone, got %v", got)
	}
}

// The message has to name which half is gone: a missing record with the route
// intact is a lost ingress write, both missing is an ordinary teardown, and
// someone reading the log at 23:00 needs to tell those apart.
func TestMissingWhatNamesTheRightHalf(t *testing.T) {
	cases := []struct {
		route, record bool
		want          string
	}{
		{false, false, "both the tunnel and DNS"},
		{false, true, "the tunnel"},
		{true, false, "DNS"},
	}
	for _, c := range cases {
		if got := missingWhat(c.route, c.record); got != c.want {
			t.Errorf("missingWhat(%v,%v) = %q, want %q", c.route, c.record, got, c.want)
		}
	}
}
