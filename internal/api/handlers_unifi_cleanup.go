package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/unifi"
)

// Cleaning up port-forward rules the panel can no longer recognise.
//
// The panel names its rules "Yggdrasil: <server> [ygg:<id8>]" and finds them
// again by searching for that tag. Two things leave rules behind it can never
// match:
//
//   - Rules from an older naming scheme, called things like "Yggdrasil:
//     25079/udp", with no tag at all. Nothing in the panel can ever match those,
//     so they are not rules that failed to be cleaned up — they are rules that
//     CANNOT be, and they accumulate forever.
//   - Tagged rules whose server is gone, when the deletion could not run: the
//     controller was unreachable, or the row was removed while the panel was
//     down.
//
// Measured on one router: 67 rules, of which 16 were orphans, several holding
// ports a server on another host now wants. That is the real cost — not
// clutter, but an invisible rule claiming a port, so the next server to want it
// gets a forward that silently loses to one nobody can see.
//
// 🔴 Scoped to THIS panel's own LAN address, and that is the whole safety
// property. Several panels share one controller here, and a rule tagged for a
// server this panel has never heard of is very likely another panel's. Without
// the address check, "clean up what I do not recognise" means "delete my
// neighbour's forwards" — a cleanup that breaks production on a machine the
// operator was not even looking at.
//
// Never automatic. It is a button that previews first and deletes on a second,
// explicit call, because the blast radius is somebody's router.

// unifiOrphans picks the rules this panel may delete: its own prefix, pointing
// at its own address, with a tag naming no server it knows — or no tag at all.
//
// Split from the HTTP and the controller so the decision can be tested, which
// matters more here than usual: everything this returns is about to be deleted.
func unifiOrphans(rules []unifi.PortForward, known map[string]bool, myIP string) []unifi.PortForward {
	var out []unifi.PortForward
	if strings.TrimSpace(myIP) == "" {
		return nil // no address to scope by: delete nothing at all
	}
	for _, r := range rules {
		if !strings.HasPrefix(r.Name, "Yggdrasil: ") {
			continue // somebody else's rule, or a hand-made one
		}
		if r.Fwd != myIP {
			continue // another host's server — very likely another panel's rule
		}
		id, ok := unifiTagID(r.Name)
		if ok && known[id] {
			continue // a server this panel still has
		}
		out = append(out, r)
	}
	return out
}

// unifiTagID extracts the 8-character server id from "... [ygg:1c7b2cb6]".
// Reports false when the rule carries no tag, which is itself a reason to
// consider it orphaned — those are exactly the legacy rules nothing can match.
func unifiTagID(name string) (string, bool) {
	i := strings.Index(name, "[ygg:")
	if i < 0 {
		return "", false
	}
	rest := name[i+len("[ygg:"):]
	j := strings.Index(rest, "]")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// knownServerIDs is the set of 8-character ids this panel has rows for. Every
// server, not just the running ones: a stopped server keeps its rule on purpose
// while the operator decides, and deleting it here would fight the panel.
func (s *Server) knownServerIDs(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM servers")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && len(id) >= 8 {
			out[id[:8]] = true
		}
	}
	return out
}

type unifiOrphanView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Proto    string `json:"proto"`
	DstPort  string `json:"dst_port"`
	Fwd      string `json:"fwd"`
	FwdPort  string `json:"fwd_port"`
	Untagged bool   `json:"untagged"`
}

// handleUnifiOrphans lists what a cleanup would delete. Always available, never
// changes anything — the operator should be able to look without committing.
func (s *Server) handleUnifiOrphans(w http.ResponseWriter, r *http.Request) {
	orphans, myIP, err := s.unifiFindOrphans(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := make([]unifiOrphanView, 0, len(orphans))
	for _, o := range orphans {
		_, tagged := unifiTagID(o.Name)
		out = append(out, unifiOrphanView{
			ID: o.ID, Name: o.Name, Proto: o.Proto, DstPort: o.DstPort,
			Fwd: o.Fwd, FwdPort: o.FwdPort, Untagged: !tagged,
		})
	}
	jsonOK(w, map[string]any{"this_host": myIP, "orphans": out})
}

// handleUnifiCleanup deletes them, reporting each outcome separately so a
// partial failure is visible rather than averaged into "done".
func (s *Server) handleUnifiCleanup(w http.ResponseWriter, r *http.Request) {
	orphans, myIP, err := s.unifiFindOrphans(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	c, cerr := s.unifiClient(r.Context())
	if cerr != nil || c == nil {
		jsonError(w, "UniFi is not configured on this panel", http.StatusBadRequest)
		return
	}
	deleted, failed := []string{}, []string{}
	for _, o := range orphans {
		if derr := c.DeletePortForward(o.ID); derr != nil {
			failed = append(failed, o.Name+": "+derr.Error())
			continue
		}
		deleted = append(deleted, o.Name)
	}
	s.auditLog(r, "unifi.cleanup_orphans", "host:"+myIP,
		map[string]any{"deleted": deleted, "failed": failed})
	jsonOK(w, map[string]any{"deleted": deleted, "failed": failed})
}

func (s *Server) unifiFindOrphans(ctx context.Context) ([]unifi.PortForward, string, error) {
	c, err := s.unifiClient(ctx)
	if err != nil {
		return nil, "", err
	}
	if c == nil {
		return nil, "", errUnifiOff
	}
	rules, lerr := c.ListPortForwards()
	if lerr != nil {
		return nil, "", lerr
	}
	myIP := firstNonEmpty(s.getSetting(ctx, "unifi_forward_host"), localLANIP())
	return unifiOrphans(rules, s.knownServerIDs(ctx), myIP), myIP, nil
}

type unifiErr string

func (e unifiErr) Error() string { return string(e) }

const errUnifiOff = unifiErr("UniFi is not configured on this panel")
