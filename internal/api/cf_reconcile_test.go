package api

import (
	"net"
	"testing"
)

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

// A route that exists but names an old address is broken, and is the quiet
// kind: present in the tunnel, present in DNS, 502 to everybody.
func TestWrongTargetNeedsWork(t *testing.T) {
	got := cfNeedsProvision([]cfHostnameState{
		{Domain: "app.example.com", HasRoute: true, HasRecord: true, WrongTarget: true},
	})
	if len(got) != 1 {
		t.Fatalf("a route pointing at the wrong address must be rewritten, got %v", got)
	}
}

// 2026-10-22: .164 rebooted onto 192.168.1.151 with cf_internal_host still
// 192.168.1.164, and every tunnel route went to an address nobody held.
func TestChooseInternalHost(t *testing.T) {
	local := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.151"), net.ParseIP("172.17.0.1")}
	cases := []struct {
		name, configured, auto string
		local                  []net.IP
		want                   string
		stale                  bool
	}{
		{"unset follows the LAN", "", "192.168.1.151", local, "192.168.1.151", false},
		{"the DHCP change", "192.168.1.164", "192.168.1.151", local, "192.168.1.151", true},
		{"a configured address we still have", "192.168.1.151", "192.168.1.151", local, "192.168.1.151", false},
		{"the docker bridge is ours too", "172.17.0.1", "192.168.1.151", local, "172.17.0.1", false},
		{"a hostname is trusted", "panel.lan", "192.168.1.151", local, "panel.lan", false},
		{"no LAN address to fall back to", "192.168.1.164", "", local, "192.168.1.164", false},
		{"interfaces unknown: trust the setting", "192.168.1.164", "192.168.1.151", nil, "192.168.1.164", false},
	}
	for _, c := range cases {
		got, stale := chooseInternalHost(c.configured, c.auto, c.local)
		if got != c.want || stale != c.stale {
			t.Errorf("%s: got %q stale=%v, want %q stale=%v", c.name, got, stale, c.want, c.stale)
		}
	}
}
