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

	"github.com/google/uuid"
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

	// Servers are written EXPANDED — servers/<name>/manifest.json followed by the
	// raw data files — never as a nested spooled archive. Every file's size is
	// known from stat, so the stream flows continuously: no temp spool, no silent
	// minutes that a reverse proxy (or an anxious admin) reads as a hang. That
	// silence killed real exports through NPM's idle timeout.
	for _, id := range serverIDs {
		if err := s.writeServerEntries(ctx, tw, id, names[id]); err != nil {
			break // stream is already committed; truncation is the failure signal
		}
	}
	tw.Close()
	gz.Close()
	s.auditLog(r, "migration.export", "panel", map[string]any{
		"groups": r.URL.Query().Get("include"), "servers": len(serverIDs)})
}

// writeServerEntries writes one server into the archive in expanded form:
// servers/<name>/manifest.json, then servers/<name>/data/**.
func (s *Server) writeServerEntries(ctx context.Context, tw *tar.Writer, id, name string) error {
	man, dataDir, err := s.buildTransferManifest(ctx, id)
	if err != nil {
		return err
	}
	prefix := "servers/" + safeFilename(name) + "/"
	manJSON, _ := json.MarshalIndent(man, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: prefix + "manifest.json", Mode: 0o600, Size: int64(len(manJSON))}); err != nil {
		return err
	}
	if _, err := tw.Write(manJSON); err != nil {
		return err
	}
	if dataDir != "" {
		if fi, err := os.Stat(dataDir); err == nil && fi.IsDir() {
			tarDataDir(tw, dataDir, prefix+"data/")
		}
	}
	return nil
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

	// Expanded-layout state: the server currently being unpacked. Its manifest
	// arrives first (our exporter guarantees the order), data files follow, and
	// the next manifest — or EOF — finalizes it.
	type pending struct {
		man     *transferManifest
		id      string
		dataDir string
		prefix  string
		skip    bool
	}
	var pend *pending
	finalize := func() {
		if pend == nil {
			return
		}
		if pend.skip {
			servers = append(servers, map[string]any{"name": pend.man.Name, "skipped": true,
				"reason": "a server with this name already exists"})
		} else if res, err := s.finalizeServerImport(ctx, pend.man, pend.id, pend.dataDir); err != nil {
			os.RemoveAll(pend.dataDir)
			servers = append(servers, map[string]any{"name": pend.man.Name, "error": err.Error()})
		} else {
			servers = append(servers, res)
		}
		pend = nil
	}

	base := s.cfg.Database.Path[:len(s.cfg.Database.Path)-len(filepath.Base(s.cfg.Database.Path))]
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if pend != nil && !pend.skip {
				os.RemoveAll(pend.dataDir)
			}
			jsonError(w, "corrupt archive", http.StatusBadRequest)
			return
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
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
		case strings.HasPrefix(name, "servers/") && strings.HasSuffix(name, "/manifest.json"):
			finalize()
			var m transferManifest
			if json.NewDecoder(io.LimitReader(tr, 16<<20)).Decode(&m) != nil ||
				m.Version < 1 || m.Version > transferVersion {
				servers = append(servers, map[string]any{"bundle": name, "error": "unsupported bundle version"})
				continue
			}
			pend = &pending{man: &m, prefix: strings.TrimSuffix(name, "manifest.json")}
			if skipExisting && s.serverNameTaken(ctx, m.Name) {
				pend.skip = true
				continue
			}
			pend.id = uuid.New().String()
			pend.dataDir = filepath.Join(base, "servers", pend.id)
		case pend != nil && strings.HasPrefix(name, pend.prefix+"data/"):
			if pend.skip {
				continue // drain without writing
			}
			rel := strings.TrimPrefix(name, pend.prefix+"data/")
			dest := filepath.Join(pend.dataDir, filepath.Clean("/"+rel))
			if hdr.Typeflag == tar.TypeDir {
				os.MkdirAll(dest, 0o755)
				continue
			}
			os.MkdirAll(filepath.Dir(dest), 0o755)
			if f, ferr := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, hdr.FileInfo().Mode()); ferr == nil {
				io.Copy(f, tr) //nolint:errcheck
				f.Close()
			}
		case strings.HasPrefix(name, "servers/") && strings.HasSuffix(name, ".tar.gz"):
			// Older archives carried each server as a nested bundle.
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
	finalize()
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
