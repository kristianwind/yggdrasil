package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Minecraft server-jar update check. Paper and Purpur ship many builds per
// Minecraft version (bug/security fixes), so a server can fall behind without any
// version change. The install records what it fetched in .ygg_jar ("<type> <ver>
// <build>"); this compares that build against the latest the upstream API offers.
// Vanilla/Fabric/Forge jars are fixed per version — no build stream — so they
// report unsupported. Applying an update re-runs Install (which fetches latest).

type jarStatus struct {
	Supported    bool   `json:"supported"`
	Type         string `json:"type,omitempty"`
	Version      string `json:"version,omitempty"`
	CurrentBuild string `json:"current_build,omitempty"`
	LatestBuild  string `json:"latest_build,omitempty"`
	Update       bool   `json:"update_available"`
	Note         string `json:"note,omitempty"`
}

// handleJarUpdate reports whether a newer Paper/Purpur build exists for a
// Minecraft-Java server's installed version.
func (s *Server) handleJarUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	var gameskillID, dataDir, envJSON string
	if s.db.QueryRowContext(r.Context(), "SELECT gameskill_id, data_dir, COALESCE(env_json,'{}') FROM servers WHERE id=?", id).
		Scan(&gameskillID, &dataDir, &envJSON) != nil || gameskillID != "minecraft-java" {
		jsonOK(w, jarStatus{Supported: false})
		return
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env) //nolint:errcheck

	typ, ver, curBuild := readJarMarker(dataDir)
	if typ == "" {
		typ = env["SERVER_TYPE"]
	}
	if ver == "" {
		ver = env["MC_VERSION"]
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ != "paper" && typ != "purpur" {
		jsonOK(w, jarStatus{Supported: false, Type: typ, Note: "Build updates are available for Paper and Purpur; other types ship one jar per version."})
		return
	}
	if curBuild == "" || ver == "" || ver == "latest" {
		jsonOK(w, jarStatus{Supported: true, Type: typ, Version: ver, Note: "Run Update/Reinstall once so the panel can record the build and check for newer ones."})
		return
	}

	latest, err := latestJarBuild(r.Context(), typ, ver)
	if err != nil || latest == "" {
		jsonOK(w, jarStatus{Supported: true, Type: typ, Version: ver, CurrentBuild: curBuild, Note: "Couldn't reach the update API right now."})
		return
	}
	jsonOK(w, jarStatus{
		Supported:    true,
		Type:         typ,
		Version:      ver,
		CurrentBuild: curBuild,
		LatestBuild:  latest,
		Update:       latest != curBuild,
	})
}

// readJarMarker parses the install's .ygg_jar file ("<type> <version> <build>").
func readJarMarker(dataDir string) (typ, ver, build string) {
	data, err := os.ReadFile(filepath.Join(dataDir, ".ygg_jar"))
	if err != nil {
		return "", "", ""
	}
	f := strings.Fields(strings.TrimSpace(string(data)))
	for i, v := range f {
		switch i {
		case 0:
			typ = v
		case 1:
			ver = v
		case 2:
			build = v
		}
	}
	return typ, ver, build
}

// latestJarBuild returns the newest build id the upstream API offers for a
// Paper/Purpur version.
func latestJarBuild(ctx context.Context, typ, ver string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	switch typ {
	case "paper":
		var out struct {
			ID    json.Number `json:"id"`
			Build json.Number `json:"build"`
		}
		if err := getJSON(ctx, "https://fill.papermc.io/v3/projects/paper/versions/"+ver+"/builds/latest", &out); err != nil {
			return "", err
		}
		if s := out.ID.String(); s != "" && s != "0" {
			return s, nil
		}
		return out.Build.String(), nil
	case "purpur":
		var out struct {
			Build string `json:"build"`
		}
		if err := getJSON(ctx, "https://api.purpurmc.org/v2/purpur/"+ver+"/latest", &out); err != nil {
			return "", err
		}
		return out.Build, nil
	}
	return "", nil
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "yggdrasil-panel")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
