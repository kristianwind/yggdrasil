package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// Rune browser — list and install community runes straight from a GitHub repo,
// instead of downloading a YAML by hand and uploading it. Defaults to this
// project's own community-runes/ directory, but any owner/repo works.

const (
	defaultRuneRepo = "kristianwind/yggdrasil"
	defaultRunePath = "community-runes"
	defaultRuneRef  = "main"
)

// Only fetch from GitHub's own hosts — keeps the install endpoint from being used
// as a generic SSRF fetch-anything proxy.
var ghAllowedHosts = map[string]bool{
	"api.github.com":                true,
	"raw.githubusercontent.com":     true,
	"github.com":                    true,
	"objects.githubusercontent.com": true,
}

var ghRepoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
var ghRefRe = regexp.MustCompile(`^[\w./-]+$`)

type ghRune struct {
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	Installed   bool   `json:"installed"`
	ParseError  string `json:"parse_error,omitempty"`
}

// ghRunesCache memoizes the (relatively expensive, rate-limited) GitHub listing +
// per-file parse so repeated opens of the browser don't burn the unauthenticated
// 60-req/hour budget. Installed-state is recomputed per request, not cached.
var (
	ghRunesMu    sync.Mutex
	ghRunesCache = map[string]ghRunesEntry{}
)

type ghRunesEntry struct {
	at    time.Time
	runes []ghRune
}

func ghHTTP(ctx context.Context, method, rawurl string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawurl, nil)
	if err != nil {
		return nil, err
	}
	// GitHub rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "yggdrasil-rune-browser")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return http.DefaultClient.Do(req)
}

// handleGithubRunes lists the *.yaml runes in a GitHub repo directory, parsing
// each for its id/name/category/description and flagging the ones already
// installed. Query params (all optional): repo=owner/name, path=dir, ref=branch.
func (s *Server) handleGithubRunes(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		repo = defaultRuneRepo
	}
	path := strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
	if path == "" {
		path = defaultRunePath
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		ref = defaultRuneRef
	}
	if !ghRepoRe.MatchString(repo) || !ghRefRe.MatchString(ref) {
		jsonError(w, "invalid repo or ref", http.StatusBadRequest)
		return
	}

	cacheKey := repo + "|" + path + "|" + ref
	refresh := r.URL.Query().Get("refresh") == "1"

	ghRunesMu.Lock()
	cached, ok := ghRunesCache[cacheKey]
	ghRunesMu.Unlock()

	var runes []ghRune
	if ok && !refresh && time.Since(cached.at) < 10*time.Minute {
		runes = cached.runes
	} else {
		var err error
		runes, err = fetchGithubRunes(r.Context(), repo, path, ref)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadGateway)
			return
		}
		ghRunesMu.Lock()
		ghRunesCache[cacheKey] = ghRunesEntry{at: time.Now(), runes: runes}
		ghRunesMu.Unlock()
	}

	// Flag installed runes (by id) — fresh each call, not from cache.
	installed := map[string]bool{}
	if rows, err := s.db.QueryContext(r.Context(), "SELECT id FROM gameskills"); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				installed[id] = true
			}
		}
		rows.Close()
	}
	out := make([]ghRune, len(runes))
	for i, g := range runes {
		g.Installed = g.ID != "" && installed[g.ID]
		out[i] = g
	}

	jsonOK(w, map[string]any{"repo": repo, "path": path, "ref": ref, "runes": out})
}

type ghDirEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "file" | "dir"
	DownloadURL string `json:"download_url"`
}

func isYAMLName(n string) bool {
	ln := strings.ToLower(n)
	return strings.HasSuffix(ln, ".yaml") || strings.HasSuffix(ln, ".yml")
}

// ghListDir fetches one directory listing from the GitHub contents API.
func ghListDir(ctx context.Context, repo, path, ref string) ([]ghDirEntry, error) {
	listURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s",
		repo, path, url.QueryEscape(ref))
	resp, err := ghHTTP(ctx, "GET", listURL, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, fmt.Errorf("not found: %s/%s@%s", repo, path, ref)
	case http.StatusForbidden:
		return nil, fmt.Errorf("github rate limit reached — try again in a few minutes")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %d", resp.StatusCode)
	}
	var listing []ghDirEntry
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("parse github listing: %w", err)
	}
	return listing, nil
}

// fetchGithubRunes lists a repo directory AND its immediate subdirectories (so
// runes can be grouped into folders like databases/ apps/ games/), then fetches +
// parses each .yaml concurrently for its metadata.
func fetchGithubRunes(ctx context.Context, repo, path, ref string) ([]ghRune, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	entries, err := ghListDir(ctx, repo, path, ref)
	if err != nil {
		return nil, err
	}
	var candidates []ghRune
	var subdirs []string
	for _, e := range entries {
		if e.Type == "file" && isYAMLName(e.Name) && e.DownloadURL != "" {
			candidates = append(candidates, ghRune{Filename: e.Name, DownloadURL: e.DownloadURL})
		} else if e.Type == "dir" {
			subdirs = append(subdirs, path+"/"+e.Name)
		}
	}
	// Descend one level into subfolders (best-effort; a failed subdir is skipped).
	for _, sd := range subdirs {
		subEntries, serr := ghListDir(ctx, repo, sd, ref)
		if serr != nil {
			continue
		}
		for _, e := range subEntries {
			if e.Type == "file" && isYAMLName(e.Name) && e.DownloadURL != "" {
				candidates = append(candidates, ghRune{Filename: e.Name, DownloadURL: e.DownloadURL})
			}
		}
	}

	// Fetch + parse each rune YAML concurrently (cap concurrency at 6).
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(g *ghRune) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := fetchGithubRaw(ctx, g.DownloadURL)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			gs, err := gameskill.Parse(data)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			g.ID, g.Name, g.Category, g.Description = gs.ID, gs.Name, gs.Category, gs.Description
		}(&candidates[i])
	}
	wg.Wait()
	sort.Slice(candidates, func(i, j int) bool {
		ni, nj := candidates[i].Name, candidates[j].Name
		if ni == "" {
			ni = candidates[i].Filename
		}
		if nj == "" {
			nj = candidates[j].Filename
		}
		return ni < nj
	})
	return candidates, nil
}

func fetchGithubRaw(ctx context.Context, rawurl string) ([]byte, error) {
	u, err := url.Parse(rawurl)
	if err != nil || u.Scheme != "https" || !ghAllowedHosts[u.Host] {
		return nil, fmt.Errorf("download url not allowed")
	}
	resp, err := ghHTTP(ctx, "GET", rawurl, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
}

// handleInstallGithubRune fetches a single rune YAML from a GitHub raw URL,
// validates it, and stores it as a (non-builtin) rune — same effect as uploading
// the file by hand.
func (s *Server) handleInstallGithubRune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DownloadURL string `json:"download_url"`
	}
	if err := decodeJSON(r, &req); err != nil || req.DownloadURL == "" {
		jsonError(w, "download_url required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data, err := fetchGithubRaw(ctx, req.DownloadURL)
	if err != nil {
		jsonError(w, "fetch: "+err.Error(), http.StatusBadGateway)
		return
	}
	gs, err := gameskill.Parse(data)
	if err != nil {
		jsonError(w, "invalid gameskill: "+err.Error(), http.StatusBadRequest)
		return
	}
	if gs.ID == "" {
		jsonError(w, "rune is missing an id", http.StatusBadRequest)
		return
	}
	if s.isBuiltinRune(r.Context(), gs.ID) {
		jsonError(w, "cannot overwrite a built-in rune; use a different id", http.StatusConflict)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
		VALUES (?,?,?,?,?,0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob
	`, gs.ID, gs.Name, gs.Category, gs.Version, string(data))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "gameskill.install_github", "gameskill:"+gs.ID, map[string]string{
		"name": gs.Name, "url": req.DownloadURL,
	})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": gs.ID, "name": gs.Name})
}
