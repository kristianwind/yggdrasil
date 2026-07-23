package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Migration archive — level 2 of host migration: the panel-settings bundle plus
// any selection of servers in ONE .tar.gz, importable on another running panel
// in one upload. Layout:
//
//	panel.json                          the settings bundle (selected groups)
//	servers/<name>.yggserver.tar.gz     one v2 server bundle per selected server
//
// Same contract as its two halves: secrets travel decrypted and are re-encrypted
// by the target, both ends admin-only, the file is a credential. Import merges —
// panel.json first (so realms/keys/channels exist), then each server, reporting
// per server what happened.

// handleMigrationExport streams the archive.
// ?include=channels,ai,... (settings groups, may be empty) &servers=id1,id2 | all
func (s *Server) handleMigrationExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	include := map[string]bool{}
	for _, g := range strings.Split(r.URL.Query().Get("include"), ",") {
		if g = strings.TrimSpace(g); g != "" {
			include[g] = true
		}
	}
	var serverIDs []string
	switch sel := strings.TrimSpace(r.URL.Query().Get("servers")); sel {
	case "":
	case "all":
		rows, err := s.db.QueryContext(ctx, "SELECT id FROM servers ORDER BY name")
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					serverIDs = append(serverIDs, id)
				}
			}
			rows.Close()
		}
	default:
		for _, id := range strings.Split(sel, ",") {
			if id = strings.TrimSpace(id); id != "" {
				serverIDs = append(serverIDs, id)
			}
		}
	}
	if len(include) == 0 && len(serverIDs) == 0 {
		jsonError(w, "pick settings groups and/or servers to export", http.StatusBadRequest)
		return
	}

	// Resolve names first (and fail fast on a bad id) — the response streams, so
	// errors after the first byte can only truncate.
	names := map[string]string{}
	for _, id := range serverIDs {
		srv, err := s.getServer(ctx, id)
		if err != nil {
			jsonError(w, "unknown server id: "+id, http.StatusBadRequest)
			return
		}
		names[id] = srv.Name
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="panel-migration.ygg.tar.gz"`)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	if len(include) > 0 {
		pj, _ := json.MarshalIndent(s.buildPanelBundle(ctx, include), "", "  ")
		tw.WriteHeader(&tar.Header{Name: "panel.json", Mode: 0o600, Size: int64(len(pj))}) //nolint:errcheck
		tw.Write(pj)                                                                       //nolint:errcheck
	}

	// A tar header needs the entry size up front, and a server bundle's size is
	// unknowable until written — so each is spooled to a temp file, sized, copied
	// in and deleted. One server at a time keeps the footprint at max(bundle).
	for _, id := range serverIDs {
		if err := s.spoolServerBundle(ctx, tw, id, names[id]); err != nil {
			break // stream is already committed; truncation is the failure signal
		}
	}
	tw.Close()
	gz.Close()
	s.auditLog(r, "migration.export", "panel", map[string]any{
		"groups": r.URL.Query().Get("include"), "servers": len(serverIDs)})
}

// spoolServerBundle writes one server's bundle into the archive via a temp
// file (tar needs the size before the content).
func (s *Server) spoolServerBundle(ctx context.Context, tw *tar.Writer, id, name string) error {
	tmp, err := os.CreateTemp("", tempSpoolPattern)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := s.writeServerBundle(ctx, tmp, id); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: fmtEntryName(name), Mode: 0o600, Size: size}); err != nil {
		return err
	}
	_, err = io.Copy(tw, tmp)
	return err
}

// handleMigrationImport reads the archive and merges it: panel.json first, then
// each server bundle. ?skip_existing=1 skips servers whose name already lives
// here (idempotent re-import). Response: the panel summary + a per-server list.
func (s *Server) handleMigrationImport(w http.ResponseWriter, r *http.Request) {
	skipExisting := r.URL.Query().Get("skip_existing") == "1"
	gz, err := gzip.NewReader(io.LimitReader(r.Body, 64<<30))
	if err != nil {
		jsonError(w, "not a valid migration archive", http.StatusBadRequest)
		return
	}
	tr := tar.NewReader(gz)
	ctx := r.Context()

	var panelSummary map[string]int
	servers := []map[string]any{}
	sawAI := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			jsonError(w, "corrupt archive", http.StatusBadRequest)
			return
		}
		name := filepath.Clean(hdr.Name)
		switch {
		case name == "panel.json":
			var b panelBundle
			if json.NewDecoder(io.LimitReader(tr, 64<<20)).Decode(&b) != nil ||
				b.Version < 1 || b.Version > panelBundleVersion {
				jsonError(w, "invalid panel.json in archive", http.StatusBadRequest)
				return
			}
			panelSummary = s.applyPanelBundle(ctx, b)
			sawAI = b.AIConfig != nil
		case strings.HasPrefix(name, "servers/") && strings.HasSuffix(name, ".tar.gz"):
			res, ierr := s.importServerBundle(ctx, tr, skipExisting)
			if ierr != nil {
				servers = append(servers, map[string]any{
					"bundle": strings.TrimPrefix(name, "servers/"), "error": ierr.Error()})
				continue // one bad server must not sink the rest of the archive
			}
			servers = append(servers, res)
		default:
			// Unknown entries are ignored, so a future archive version stays
			// importable here for the parts this panel understands.
		}
	}
	if sawAI {
		s.startDiscordBot()
	}
	s.auditLog(r, "migration.import", "panel", map[string]any{"servers": len(servers)})
	resp := map[string]any{"servers": servers}
	if panelSummary != nil {
		resp["panel"] = panelSummary
	}
	jsonOK(w, resp)
}

// tempSpoolPattern keeps migration spool files identifiable (and sweepable).
const tempSpoolPattern = "ygg-migrate-*.tar.gz"

func fmtEntryName(name string) string {
	return "servers/" + safeFilename(name) + ".yggserver.tar.gz"
}
