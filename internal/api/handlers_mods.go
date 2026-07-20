package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/modrinth"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// modProfile describes how a Minecraft server's software maps onto Modrinth: the
// loader categories to search (a Paper server can load Spigot/Bukkit plugins too,
// so search the whole family), the single loader used to resolve a download, and
// the folder a jar drops into. vanilla has no mod support.
type modProfile struct {
	Loaders []string // the loader family: OR'd in search and version resolution
	Folder  string   // "mods" or "plugins", relative to the server data dir
}

// Display returns the primary loader name for the UI ("Fabric mods for 1.20.1").
func (p modProfile) Display() string { return p.Loaders[0] }

// modProfiles maps the minecraft-java rune's SERVER_TYPE values. Mods (Fabric/
// Forge) live in mods/; plugins (Paper/Purpur, which also load Spigot/Bukkit) in
// plugins/.
var modProfiles = map[string]modProfile{
	"fabric":   {[]string{"fabric"}, "mods"},
	"forge":    {[]string{"forge"}, "mods"},
	"neoforge": {[]string{"neoforge"}, "mods"},
	"quilt":    {[]string{"quilt", "fabric"}, "mods"},
	"paper":    {[]string{"paper", "spigot", "bukkit"}, "plugins"},
	"purpur":   {[]string{"purpur", "paper", "spigot", "bukkit"}, "plugins"},
}

// modProfileFor returns the Modrinth profile for a server's SERVER_TYPE, and
// whether that software supports mods at all (vanilla and unknown types don't).
func modProfileFor(serverType string) (modProfile, bool) {
	p, ok := modProfiles[strings.ToLower(strings.TrimSpace(serverType))]
	return p, ok
}

// modGameVersion returns the concrete Minecraft version to filter by, or "" when
// it can't be pinned (MC_VERSION "latest" or blank) so search stays unfiltered on
// version rather than returning nothing.
func modGameVersion(env map[string]string) string {
	v := strings.TrimSpace(env["MC_VERSION"])
	if v == "" || strings.EqualFold(v, "latest") {
		return ""
	}
	return v
}

// handleModSearch searches Modrinth for mods/plugins compatible with this
// server's loader and version. Read-only, so it gates on ServerView.
func (s *Server) handleModSearch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "could not load server", http.StatusInternalServerError)
		return
	}
	profile, ok := modProfileFor(rt.env["SERVER_TYPE"])
	if !ok {
		// Vanilla or a non-Minecraft rune — no mod folder to manage.
		jsonError(w, "this server type doesn't support mods or plugins", http.StatusBadRequest)
		return
	}
	gameVersion := modGameVersion(rt.env)
	hits, err := modrinth.Search(r.Context(), r.URL.Query().Get("q"), profile.Loaders, gameVersion, 30)
	if err != nil {
		jsonError(w, "mod search failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{
		"results":      hits,
		"loader":       profile.Display(),
		"folder":       profile.Folder,
		"game_version": gameVersion, // "" means "any version" (couldn't pin MC_VERSION)
	})
}

// Indirected so tests can drive installOne's recursion without the network.
var (
	modResolveVersion = modrinth.ResolveVersion
	modFetchFile      = modrinth.FetchFile
)

// maxDependencyInstalls bounds the recursive dependency walk — a guard against a
// pathological or hostile dependency graph, well above any real mod's needs.
const maxDependencyInstalls = 50

// installOne resolves the newest compatible version of a Modrinth project,
// downloads its jar into the server's mod/plugin folder (checksum-verified), and
// recurses into required dependencies. seen dedupes by project so a diamond graph
// or a cycle is walked once. It appends every filename it writes to *installed.
func (s *Server) installOne(ctx context.Context, dataDir string, p modProfile, gameVersion, project string, seen map[string]bool, installed *[]string) error {
	if seen[project] {
		return nil
	}
	seen[project] = true
	if len(seen) > maxDependencyInstalls {
		return fmt.Errorf("too many dependencies (over %d) — aborting", maxDependencyInstalls)
	}
	v, err := modResolveVersion(ctx, project, p.Loaders, gameVersion)
	if err == modrinth.ErrNotFound {
		return fmt.Errorf("no build of %q for %s %s", project, p.Display(), orAny(gameVersion))
	}
	if err != nil {
		return err
	}
	f, err := v.PrimaryFile()
	if err != nil {
		return err
	}
	data, err := modFetchFile(ctx, f)
	if err != nil {
		return err
	}
	dest, ok := safeJoin(dataDir, p.Folder+"/"+filepath.Base(f.Filename))
	if !ok {
		return fmt.Errorf("unsafe filename %q", f.Filename)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	*installed = append(*installed, filepath.Base(f.Filename))
	// Required dependencies only — optional ones are the operator's choice.
	for _, dep := range v.Dependencies {
		if dep.DependencyType == "required" && dep.ProjectID != "" {
			if err := s.installOne(ctx, dataDir, p, gameVersion, dep.ProjectID, seen, installed); err != nil {
				return err
			}
		}
	}
	return nil
}

func orAny(v string) string {
	if v == "" {
		return "(any version)"
	}
	return v
}

// handleModInstall installs a Modrinth project (and its required dependencies)
// into the server's mods/ or plugins/ folder. Writes files, so it needs
// ServerFiles. Mods take effect on the next restart (a recreate), which the
// caller is told rather than being surprised by a no-op.
func (s *Server) handleModInstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Project string `json:"project"` // Modrinth id or slug
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Project) == "" {
		jsonError(w, "project id or slug required", http.StatusBadRequest)
		return
	}
	dataDir, ok := s.serverDataDir(w, r) // enforces ServerFiles + resolves data_dir
	if !ok {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "could not load server", http.StatusInternalServerError)
		return
	}
	profile, ok := modProfileFor(rt.env["SERVER_TYPE"])
	if !ok {
		jsonError(w, "this server type doesn't support mods or plugins", http.StatusBadRequest)
		return
	}
	var installed []string
	if err := s.installOne(r.Context(), dataDir, profile, modGameVersion(rt.env), strings.TrimSpace(req.Project), map[string]bool{}, &installed); err != nil {
		jsonError(w, "install failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "mod.install", "server:"+id, map[string]any{"project": req.Project, "files": installed})
	jsonOK(w, map[string]any{"installed": installed, "restart_required": true})
}

// handleModRemove deletes a jar from the server's mod/plugin folder by filename.
// ServerFiles-gated; safeJoin blocks any path outside the folder.
func (s *Server) handleModRemove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// The filename is a query param, not a path segment: mod jars contain '+' and
	// '.', which a path segment mangles (chi leaves %2B encoded), so removing by
	// path 404s on the very files it wrote. Query values decode cleanly.
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("file")))
	if name == "" || name == "." || !strings.HasSuffix(name, ".jar") {
		jsonError(w, "a .jar filename is required", http.StatusBadRequest)
		return
	}
	dataDir, ok := s.serverDataDir(w, r)
	if !ok {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "could not load server", http.StatusInternalServerError)
		return
	}
	profile, ok := modProfileFor(rt.env["SERVER_TYPE"])
	if !ok {
		jsonError(w, "this server type doesn't support mods or plugins", http.StatusBadRequest)
		return
	}
	dest, ok := safeJoin(dataDir, profile.Folder+"/"+name)
	if !ok {
		jsonError(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if err := os.Remove(dest); err != nil {
		if os.IsNotExist(err) {
			jsonError(w, "not installed", http.StatusNotFound)
			return
		}
		jsonError(w, "remove failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "mod.remove", "server:"+id, map[string]any{"file": name})
	jsonOK(w, map[string]any{"removed": name, "restart_required": true})
}
