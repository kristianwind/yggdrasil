package api

import (
	"context"
	"log"
	"strings"
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
// one ListIngress call and one DNS lookup per hostname, and changes nothing.

// cfHostnameState is what reconciliation needs to know about one hostname.
type cfHostnameState struct {
	Domain    string
	HasRoute  bool
	HasRecord bool
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
		if !st.HasRoute || !st.HasRecord {
			out = append(out, st.Domain)
		}
	}
	return out
}

// cfReconcileHostnames restores any hostname a running server should have.
func (s *Server) cfReconcileHostnames(ctx context.Context) {
	defer recoverLog("cfReconcileHostnames")

	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return // Cloudflare not configured here; nothing to reconcile against
	}
	internalHost := firstNonEmpty(s.getSetting(ctx, "cf_internal_host"), localLANIP())
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
	for _, in := range ingress {
		routed[strings.ToLower(in.Hostname)] = true
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
			if zid, zerr := c.ZoneForHost(domain); zerr == nil && zid != "" {
				c.SetZoneID(zid)
			}
			rec, derr := c.FindDNSRecord(domain)
			if derr != nil {
				// Could not ask is not the same as not there. Writing on a
				// failed lookup is how a reconciler turns an API hiccup into a
				// storm of unnecessary config PUTs.
				log.Printf("cloudflare: could not check DNS for %q, leaving it alone: %v", domain, derr)
				continue
			}
			need := cfNeedsProvision([]cfHostnameState{{
				Domain:    domain,
				HasRoute:  routed[strings.ToLower(domain)],
				HasRecord: rec,
			}})
			if len(need) == 0 {
				continue
			}
			log.Printf("cloudflare: %s (%s) should serve %q but it is missing from %s — restoring",
				x.name, x.id, domain, missingWhat(routed[strings.ToLower(domain)], rec))
			s.cfApplyRoute(ctx, c, x.id, rt, internalHost)
			restored++
		}
	}
	if restored > 0 {
		log.Printf("cloudflare: restored %d hostname(s) that were missing", restored)
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
