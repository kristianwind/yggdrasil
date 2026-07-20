package api

import (
	"net/http"
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
	SearchLoaders []string // OR'd in the Modrinth facet
	InstallLoader string   // used when resolving a version to download
	Folder        string   // "mods" or "plugins", relative to the server data dir
}

// modProfiles maps the minecraft-java rune's SERVER_TYPE values. Mods (Fabric/
// Forge) live in mods/; plugins (Paper/Purpur, which also load Spigot/Bukkit) in
// plugins/.
var modProfiles = map[string]modProfile{
	"fabric":   {[]string{"fabric"}, "fabric", "mods"},
	"forge":    {[]string{"forge"}, "forge", "mods"},
	"neoforge": {[]string{"neoforge"}, "neoforge", "mods"},
	"quilt":    {[]string{"quilt", "fabric"}, "quilt", "mods"},
	"paper":    {[]string{"paper", "spigot", "bukkit"}, "paper", "plugins"},
	"purpur":   {[]string{"purpur", "paper", "spigot", "bukkit"}, "purpur", "plugins"},
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
	hits, err := modrinth.Search(r.Context(), r.URL.Query().Get("q"), profile.SearchLoaders, gameVersion, 30)
	if err != nil {
		jsonError(w, "mod search failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{
		"results":      hits,
		"loader":       profile.InstallLoader,
		"folder":       profile.Folder,
		"game_version": gameVersion, // "" means "any version" (couldn't pin MC_VERSION)
	})
}
