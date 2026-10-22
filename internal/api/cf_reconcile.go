package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// Putting back what should be there.
//
// The panel provisions a hostname when a server is started and removes it when
// the server stops. That is a pair of edges, and an edge-driven design has no
// answer to "the record is gone and nothing is about to happen" — which is the
// state the fleet was left in on 2026-09-16, when a cold boot tore down eleven
// names for servers that were running fine.
//
// Fixing the teardown stops it happening again. It does not put the names back:
// the servers are already marked running, so nothing transitions, so nothing
// re-provisions. Somebody would have to restart ten production services to
// repair a fault they never caused, which is not a repair anyone should have to
// perform by hand.
//
// So the panel now also reconciles by STATE at startup, once Docker is known to
// be up: for every running server, the hostnames it should have are compared
// against what Cloudflare actually has, and only the gap is written. That heals
// the existing damage on the next panel restart, with no container touched and
// nobody disconnected — and it makes any future divergence self-correcting,
// whatever caused it.
//
// It writes only what is missing. A fleet whose records are all present costs
// one ListIngress call, one zone listing and one DNS lookup per hostname, and
// changes nothing.
//
// It runs every cfReconcileInterval, not only at startup. Startup-only meant a
// name lost while the panel was up stayed lost until the panel next restarted:
// on 2026-10-20 app.getcellarius.com was gone for eight hours after a server
// restart, on a panel that had been up for two days.

// cfReconcileInterval is how long a missing hostname can stay missing.
const cfReconcileInterval = 10 * time.Minute

// cfReconcileMu keeps two passes from running at once — the startup pass can
// still be working through a large fleet when the first tick fires.
var cfReconcileMu sync.Mutex

// cfReconcileLoop runs the reconcile now and then every cfReconcileInterval.
func (s *Server) cfReconcileLoop(ctx context.Context) {
	for {
		s.cfReconcileHostnames(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfReconcileInterval):
		}
	}
}

// cfHostnameState is what reconciliation needs to know about one hostname.
type cfHostnameState struct {
	Domain    string
	HasRoute  bool
	HasRecord bool
	// WrongTarget: the route exists but sends traffic somewhere other than
	// where this server now listens — an old LAN address after a DHCP change,
	// or an old port.
	WrongTarget bool
}

// cfNeedsProvision decides which hostnames to write. Split out from the API
// calls so the decision is testable: the expensive half is Cloudflare, and the
// half that can be wrong is this.
//
// A hostname with a route but no DNS record still needs the work — that is
// exactly the state the lost ingress writes left four names in, and treating
// "route exists" as "provisioned" would skip precisely the ones that are broken.
func cfNeedsProvision(states []cfHostnameState) []string {
	var out []string
	for _, st := range states {
		if st.Domain == "" {
			continue
		}
		if !st.HasRoute || !st.HasRecord || st.WrongTarget {
			out = append(out, st.Domain)
		}
	}
	return out
}

// cfReconcileHostnames restores any hostname a running server should have.
func (s *Server) cfReconcileHostnames(ctx context.Context) {
	defer recoverLog("cfReconcileHostnames")
	if !cfReconcileMu.TryLock() {
		return
	}
	defer cfReconcileMu.Unlock()

	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return // Cloudflare not configured here; nothing to reconcile against
	}
	internalHost := s.cfInternalHost(ctx)
	if internalHost == "" {
		// Provisioning would produce an ingress rule pointing nowhere. Say so:
		// this is also why a panel can look configured and quietly do nothing.
		log.Printf("cloudflare: cannot reconcile hostnames — no internal host set and no LAN IP found")
		return
	}

	// One call for the whole tunnel rather than one per hostname.
	ingress, err := c.ListIngress()
	if err != nil {
		log.Printf("cloudflare: could not read the tunnel's routes, skipping reconciliation: %v", err)
		return
	}
	routed := make(map[string]bool, len(ingress))
	target := make(map[string]string, len(ingress))
	for _, in := range ingress {
		routed[strings.ToLower(in.Hostname)] = true
		target[strings.ToLower(in.Hostname)] = in.Service
	}

	rows, err := s.db.QueryContext(ctx, "SELECT id, name FROM servers WHERE status='running'")
	if err != nil {
		return
	}
	type sv struct{ id, name string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.name) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	var restored int
	for _, x := range list {
		for _, rt := range s.serverRoutes(ctx, x.id) {
			domain := s.cfFullDomain(ctx, rt.Hostname)
			if domain == "" {
				continue
			}
			// The DNS record lives in the hostname's own zone, which is not
			// necessarily the panel's default one.
			if zerr := c.UseZoneFor(domain); zerr != nil {
				log.Printf("cloudflare: could not find the zone for %q, leaving it alone: %v", domain, zerr)
				continue
			}
			rec, derr := c.FindDNSRecord(domain)
			if derr != nil {
				// Could not ask is not the same as not there. Writing on a
				// failed lookup is how a reconciler turns an API hiccup into a
				// storm of unnecessary config PUTs.
				log.Printf("cloudflare: could not check DNS for %q, leaving it alone: %v", domain, derr)
				continue
			}
			// A route that exists but points at the wrong place is as broken as
			// a missing one, and quieter: on 2026-10-22 .164 came back from a
			// reboot on a new DHCP address and every route still named the old
			// one — all present, all 502.
			want := ""
			if port := s.serverRoutePort(ctx, x.id, rt.PortName); port != 0 {
				want = fmt.Sprintf("http://%s:%d", internalHost, port)
			}
			have := target[strings.ToLower(domain)]
			wrong := routed[strings.ToLower(domain)] && want != "" && !strings.EqualFold(have, want)
			need := cfNeedsProvision([]cfHostnameState{{
				Domain:      domain,
				HasRoute:    routed[strings.ToLower(domain)],
				HasRecord:   rec,
				WrongTarget: wrong,
			}})
			if len(need) == 0 {
				continue
			}
			// The list was read before the Cloudflare calls above, which take
			// a while across a fleet. A server stopped since then is having
			// its names removed right now; restoring them would undo the stop.
			var status string
			s.db.QueryRowContext(ctx, "SELECT status FROM servers WHERE id=?", x.id).Scan(&status) //nolint:errcheck
			if status != "running" {
				continue
			}
			if wrong && rec {
				log.Printf("cloudflare: %s (%s) serves %q from %s but listens on %s — repointing",
					x.name, x.id, domain, have, want)
			} else {
				log.Printf("cloudflare: %s (%s) should serve %q but it is missing from %s — restoring",
					x.name, x.id, domain, missingWhat(routed[strings.ToLower(domain)], rec))
			}
			s.cfApplyRoute(ctx, c, x.id, rt, internalHost)
			restored++
		}
	}
	if restored > 0 {
		log.Printf("cloudflare: restored %d hostname(s) that were missing or pointed at the wrong place", restored)
	}
}

// missingWhat names the half that is gone, because the two have different
// causes: a missing DNS record with the route intact is a lost ingress write,
// while both missing is an ordinary teardown.
func missingWhat(hasRoute, hasRecord bool) string {
	switch {
	case !hasRoute && !hasRecord:
		return "both the tunnel and DNS"
	case !hasRoute:
		return "the tunnel"
	default:
		return "DNS"
	}
}

// cfInternalHost is the address the tunnel's connector reaches this machine's
// server ports on.
//
// The setting wins, with one exception: an IP literal that is not an address of
// this machine. Every server the panel routes runs here, so such a value can
// only be stale — and it is exactly what a DHCP change leaves behind. On
// 2026-10-22 .164 rebooted, came back as 192.168.1.151, and cf_internal_host
// still said 192.168.1.164: every tunnel site on the box answered 502 while the
// containers behind them were fine. Following the address the machine actually
// has turns that into a self-correcting blip at the next reconcile.
func (s *Server) cfInternalHost(ctx context.Context) string {
	configured := strings.TrimSpace(s.getSetting(ctx, "cf_internal_host"))
	host, stale := chooseInternalHost(configured, localLANIP(), localAddrs())
	if stale {
		log.Printf("cloudflare: cf_internal_host is %s, which is not an address of this machine any more — using %s, its current LAN address. Update the setting (or reserve the address in DHCP) to silence this.", configured, host)
	}
	return host
}

// chooseInternalHost is cfInternalHost's decision, without the machine. stale
// reports that the configured value was set aside.
func chooseInternalHost(configured, auto string, local []net.IP) (host string, stale bool) {
	if configured == "" {
		return auto, false
	}
	ip := net.ParseIP(configured)
	if ip == nil || auto == "" || len(local) == 0 {
		return configured, false // a hostname, or nothing to compare against: trust it
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return configured, false
	}
	for _, l := range local {
		if l.Equal(ip) {
			return configured, false
		}
	}
	return auto, true
}

func localAddrs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}
