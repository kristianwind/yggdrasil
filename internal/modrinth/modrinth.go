// Package modrinth is a small read-only client for the Modrinth API
// (https://modrinth.com), the mod/plugin index behind Yggdrasil's one-click
// Minecraft mod manager. It searches for projects compatible with a server's
// loader and game version and resolves the exact file to download.
//
// Modrinth requires a descriptive User-Agent and rejects requests without one,
// so every request sets it. The API is free and unauthenticated for the read
// endpoints used here. Only project_type "mod" exists for both Fabric/Forge mods
// and Paper/Spigot plugins — the loader (a "category") is what tells them apart,
// so callers filter by loader, never by project_type.
package modrinth

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBase = "https://api.modrinth.com/v2"

// userAgent identifies the panel to Modrinth as their docs require.
const userAgent = "kristianwind/yggdrasil (Minecraft mod manager)"

var httpClient = &http.Client{Timeout: 20 * time.Second}

// baseURL is overridable in tests.
var baseURL = apiBase

// Project is one search hit or a fetched project. Search hits carry project_id;
// the /projects endpoint carries id — both are captured so either shape parses.
type Project struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Downloads   int      `json:"downloads"`
	IconURL     string   `json:"icon_url"`
	Categories  []string `json:"categories"`
}

// File is one downloadable artifact of a version.
type File struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Hashes   struct {
		SHA512 string `json:"sha512"`
	} `json:"hashes"`
}

// Dependency links a version to another project it needs. DependencyType is
// "required", "optional", "incompatible" or "embedded".
type Dependency struct {
	ProjectID      string `json:"project_id"`
	VersionID      string `json:"version_id"`
	DependencyType string `json:"dependency_type"`
}

// Version is one release of a project for a set of loaders and game versions.
type Version struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	VersionNumber string       `json:"version_number"`
	Files         []File       `json:"files"`
	Dependencies  []Dependency `json:"dependencies"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
	DatePublished time.Time    `json:"date_published"`
}

func get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("modrinth: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// ErrNotFound is returned when a project or version doesn't exist.
var ErrNotFound = fmt.Errorf("modrinth: not found")

func postJSON(ctx context.Context, path string, reqBody, out any) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("modrinth: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// LookupByHashes identifies installed jars: it maps each sha512 to the Modrinth
// Version that file belongs to (giving its project and installed version).
// Unknown hashes are simply absent from the result — a hand-installed jar.
func LookupByHashes(ctx context.Context, sha512s []string) (map[string]Version, error) {
	out := map[string]Version{}
	if len(sha512s) == 0 {
		return out, nil
	}
	err := postJSON(ctx, "/version_files", map[string]any{"hashes": sha512s, "algorithm": "sha512"}, &out)
	return out, err
}

// LatestByHashes returns, per installed sha512, the newest version for the given
// loaders and gameVersion — the purpose-built update check. A hash whose latest
// build is itself just maps back to the same version (i.e. up to date).
func LatestByHashes(ctx context.Context, sha512s, loaders []string, gameVersion string) (map[string]Version, error) {
	out := map[string]Version{}
	if len(sha512s) == 0 || gameVersion == "" {
		return out, nil // can't pin updates without a concrete game version
	}
	body := map[string]any{"hashes": sha512s, "algorithm": "sha512", "loaders": loaders, "game_versions": []string{gameVersion}}
	err := postJSON(ctx, "/version_files/update", body, &out)
	return out, err
}

// GetProjects fetches project metadata (title, slug, icon) for the given ids, so
// installed jars can be shown by name rather than by hash.
func GetProjects(ctx context.Context, ids []string) (map[string]Project, error) {
	out := map[string]Project{}
	if len(ids) == 0 {
		return out, nil
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	q := url.Values{}
	q.Set("ids", "["+strings.Join(quoted, ",")+"]") // Encode escapes the [ ] " chars
	var list []Project
	if err := get(ctx, "/projects?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	for _, p := range list {
		out[p.ID] = p
	}
	return out, nil
}

// facets builds a Modrinth facet array: an AND of OR-groups. Each group here is
// a single dimension (loaders OR'd together, one game version).
func facets(loaders []string, gameVersion string) string {
	var groups []string
	if len(loaders) > 0 {
		var ors []string
		for _, l := range loaders {
			ors = append(ors, fmt.Sprintf("%q", "categories:"+l))
		}
		groups = append(groups, "["+strings.Join(ors, ",")+"]")
	}
	if gameVersion != "" {
		groups = append(groups, fmt.Sprintf("[%q]", "versions:"+gameVersion))
	}
	return "[" + strings.Join(groups, ",") + "]"
}

// Search finds projects matching query, compatible with any of loaders and (if
// non-empty) gameVersion. loaders are Modrinth loader categories ("fabric",
// "paper", …). Results are ordered by relevance, capped at limit (max 100).
func Search(ctx context.Context, query string, loaders []string, gameVersion string, limit int) ([]Project, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("index", "relevance")
	if f := facets(loaders, gameVersion); f != "[]" {
		q.Set("facets", f)
	}
	var out struct {
		Hits []Project `json:"hits"`
	}
	if err := get(ctx, "/search?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Hits, nil
}

// ResolveVersion returns the newest version of idOrSlug that supports any of
// loaders and (if non-empty) gameVersion. loaders is a family (a Paper server
// also loads Spigot/Bukkit plugins), so pass them all. Returns ErrNotFound when
// nothing matches, so the caller can tell "no compatible build" from a network
// error.
func ResolveVersion(ctx context.Context, idOrSlug string, loaders []string, gameVersion string) (*Version, error) {
	q := url.Values{}
	if len(loaders) > 0 {
		var quoted []string
		for _, l := range loaders {
			quoted = append(quoted, fmt.Sprintf("%q", l))
		}
		q.Set("loaders", "["+strings.Join(quoted, ",")+"]")
	}
	if gameVersion != "" {
		q.Set("game_versions", fmt.Sprintf("[%q]", gameVersion))
	}
	var versions []Version
	if err := get(ctx, "/project/"+url.PathEscape(idOrSlug)+"/version?"+q.Encode(), &versions); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrNotFound
	}
	// The API returns newest first, but don't rely on it — pick the latest by date.
	best := &versions[0]
	for i := range versions {
		if versions[i].DatePublished.After(best.DatePublished) {
			best = &versions[i]
		}
	}
	return best, nil
}

// PrimaryFile returns the version's primary downloadable file (the mod jar). It
// falls back to the first file when none is flagged primary, and errors only
// when the version carries no files at all.
func (v *Version) PrimaryFile() (File, error) {
	for _, f := range v.Files {
		if f.Primary {
			return f, nil
		}
	}
	if len(v.Files) > 0 {
		return v.Files[0], nil
	}
	return File{}, fmt.Errorf("modrinth: version %s has no files", v.ID)
}

// cdnHost is the only host FetchFile will download from. The file URLs come from
// the API (which we trust), but pinning the host is defence in depth: a tampered
// or unexpected response can't make the panel fetch an arbitrary URL (SSRF). A
// var, not a const, only so tests can point it at a local server.
var cdnHost = "cdn.modrinth.com"

// maxFileBytes caps a mod download. Individual mods and plugins are small; this
// is generous headroom, not a target, and stops a runaway or hostile response
// from filling the disk.
var maxFileBytes = 100 << 20 // 100 MiB

// FetchIcon fetches a mod's icon from the Modrinth CDN so the panel can serve it
// from its own origin — the strict CSP blocks external images, and proxying keeps
// the viewer's IP from reaching Modrinth. Same host pin as FetchFile; capped
// small since icons are tiny. Returns the content type and bytes.
func FetchIcon(ctx context.Context, rawurl string) (string, []byte, error) {
	u, err := url.Parse(rawurl)
	if err != nil || u.Scheme != "https" || u.Host != cdnHost {
		return "", nil, fmt.Errorf("modrinth: refusing to fetch icon from %q (only %s)", rawurl, cdnHost)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("modrinth: icon HTTP %d", resp.StatusCode)
	}
	const maxIcon = 4 << 20 // 4 MiB — icons are far smaller
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIcon+1))
	if err != nil {
		return "", nil, err
	}
	if len(data) > maxIcon {
		return "", nil, fmt.Errorf("modrinth: icon too large")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
	}
	return ct, data, nil
}

// FetchFile downloads f from the Modrinth CDN and returns its bytes, refusing any
// other host, capping the size, and verifying the SHA-512 the API published. A
// file with no published hash is refused rather than trusted — an unverified
// binary dropped into a server is exactly what this guards against.
func FetchFile(ctx context.Context, f File) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme != "https" || u.Host != cdnHost {
		return nil, fmt.Errorf("modrinth: refusing to download from %q (only %s)", f.URL, cdnHost)
	}
	if f.Hashes.SHA512 == "" {
		return nil, fmt.Errorf("modrinth: %s has no published checksum — refusing to install unverified", f.Filename)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("modrinth: download HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxFileBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFileBytes {
		return nil, fmt.Errorf("modrinth: %s exceeds the %d MiB limit", f.Filename, maxFileBytes>>20)
	}
	sum := sha512.Sum512(body)
	if got := hex.EncodeToString(sum[:]); got != f.Hashes.SHA512 {
		return nil, fmt.Errorf("modrinth: checksum mismatch for %s (corrupt or tampered download)", f.Filename)
	}
	return body, nil
}
