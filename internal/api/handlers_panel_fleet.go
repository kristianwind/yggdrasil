package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// One list of every server across every panel you have linked.
//
// The pain this removes is logging into four panels to see four lists. It is
// deliberately NOT a controller/target rewrite: each host keeps running its own
// full panel, so backups, the file browser, installs and everything else that
// reaches for the local disk keep working where the files actually are. This
// aggregates over the HTTP API those panels already speak.
//
// The failure mode that matters is a panel being down. One unreachable host
// must degrade to a greyed row and nothing more — a combined view that breaks
// when any one of four machines is rebooting is worse than four bookmarks.
// Hence: every panel is queried in parallel, each with its own timeout, and an
// error is reported per panel rather than failing the request.

// fleetHTTP is separate from remoteHTTP (which has no overall timeout, because
// a transfer streams gigabytes). A list call that has not answered in 10s is a
// panel that is down as far as this page is concerned.
var fleetHTTP = &http.Client{Timeout: 10 * time.Second}

type fleetPanel struct {
	ID      string           `json:"id"` // "" for this panel
	Name    string           `json:"name"`
	URL     string           `json:"url,omitempty"`
	Local   bool             `json:"local,omitempty"`
	Error   string           `json:"error,omitempty"`
	Servers []map[string]any `json:"servers"`
}

func (s *Server) handleFleetPanels(w http.ResponseWriter, r *http.Request) {
	type conn struct{ id, name, url, enc string }
	var conns []conn
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, url, COALESCE(token_enc,'') FROM remote_panels ORDER BY name")
	if err == nil {
		for rows.Next() {
			var c conn
			if rows.Scan(&c.id, &c.name, &c.url, &c.enc) == nil {
				conns = append(conns, c)
			}
		}
		rows.Close()
	}

	out := make([]fleetPanel, len(conns)+1)
	// This panel first, and without an HTTP round trip to itself.
	out[0] = fleetPanel{
		Name:    firstNonEmpty(s.getSetting(r.Context(), "panel_name"), "This panel"),
		Local:   true,
		Servers: s.localFleetServers(r),
	}

	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(i int, c conn) {
			defer wg.Done()
			defer recoverLog("handleFleetPanels")
			p := fleetPanel{ID: c.id, Name: c.name, URL: c.url, Servers: []map[string]any{}}
			if c.enc == "" {
				p.Error = "no token saved for this connection"
				out[i+1] = p
				return
			}
			token, derr := s.cipher.Decrypt(c.enc)
			if derr != nil {
				p.Error = "the saved token could not be decrypted — re-save it"
				out[i+1] = p
				return
			}
			servers, ferr := fetchRemoteServers(c.url, token)
			if ferr != nil {
				p.Error = ferr.Error()
			} else {
				p.Servers = servers
			}
			out[i+1] = p
		}(i, c)
	}
	wg.Wait()

	for i := range out {
		sort.Slice(out[i].Servers, func(a, b int) bool {
			return asString(out[i].Servers[a]["name"]) < asString(out[i].Servers[b]["name"])
		})
	}
	jsonOK(w, out)
}

// localFleetServers reuses this panel's own list handler rather than a second
// query, so the local rows carry exactly the same fields as the remote ones and
// the UI needs no special case.
func (s *Server) localFleetServers(r *http.Request) []map[string]any {
	rec := newCapture()
	s.handleListServers(rec, r)
	var list []map[string]any
	if json.Unmarshal(rec.body.Bytes(), &list) != nil {
		return []map[string]any{}
	}
	return list
}

func fetchRemoteServers(base, token string) ([]map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/api/servers", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yggdrasil-panel-fleet")
	resp, err := fleetHTTP.Do(req)
	if err != nil {
		return nil, errUnreachable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errTokenRejected
	default:
		return nil, errRemoteStatus(resp.StatusCode)
	}
	var list []map[string]any
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return nil, errUnreadable
	}
	return list, nil
}

// handleFleetAction proxies one control action to a server on a linked panel.
// The remote panel authenticates and authorises it as always — a link token is
// a ceiling on what the controller may ask for, not a way past the host's own
// rules.
func (s *Server) handleFleetAction(w http.ResponseWriter, r *http.Request) {
	remoteID := chi.URLParam(r, "id")
	serverID := chi.URLParam(r, "serverID")
	action := chi.URLParam(r, "action")
	switch action {
	case "start", "stop", "restart", "safe-restart":
	default:
		jsonError(w, "unsupported action", http.StatusBadRequest)
		return
	}
	base, token, err := s.resolveRemote(r.Context(), remoteID, "", "")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/servers/"+serverID+"/"+action, nil)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yggdrasil-panel-fleet")
	resp, err := fleetHTTP.Do(req)
	if err != nil {
		jsonError(w, "could not reach that panel", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Pass the remote's own refusal through rather than inventing one: its
		// RBAC, its scope check, its reason.
		jsonError(w, "the other panel refused: HTTP "+http.StatusText(resp.StatusCode), resp.StatusCode)
		return
	}
	s.auditLog(r, "panel.fleet_action", "remote:"+remoteID,
		map[string]any{"server": serverID, "action": action})
	jsonOK(w, map[string]any{"ok": true, "action": action})
}

// --- small helpers -----------------------------------------------------------

type fleetError string

func (e fleetError) Error() string { return string(e) }

const (
	errUnreachable   = fleetError("could not reach this panel")
	errTokenRejected = fleetError("this panel rejected the saved token — it may have been deleted there")
	errUnreadable    = fleetError("this panel's answer could not be read")
)

func errRemoteStatus(code int) error {
	return fleetError("this panel answered HTTP " + http.StatusText(code))
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// capture is an http.ResponseWriter that keeps the body, so one handler can be
// called for its JSON without a network round trip.
type capture struct {
	header http.Header
	body   *bytes.Buffer
	code   int
}

func newCapture() *capture {
	return &capture{header: http.Header{}, body: &bytes.Buffer{}, code: http.StatusOK}
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *capture) WriteHeader(code int)        { c.code = code }
