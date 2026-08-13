package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// parseRawGithubSource pulls the owner/repo, directory and ref out of a raw
// GitHub download URL (https://raw.githubusercontent.com/owner/repo/ref/dir/file).
// Returns empty strings if it isn't a recognizable raw URL (e.g. a hand-uploaded
// rune), in which case the rune simply isn't checked against a source repo.
func parseRawGithubSource(raw string) (repo, path, ref string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host != "raw.githubusercontent.com" {
		return "", "", ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) < 4 { // owner, repo, ref, file (path optional)
		return "", "", ""
	}
	repo = seg[0] + "/" + seg[1]
	ref = seg[2]
	if len(seg) > 4 { // everything between the ref and the filename is the dir
		path = strings.Join(seg[3:len(seg)-1], "/")
	}
	if !ghRepoRe.MatchString(repo) || !ghRefRe.MatchString(ref) {
		return "", "", ""
	}
	return repo, path, ref
}

type ghRune struct {
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version,omitempty"` // the repo copy's version
	Installed   bool   `json:"installed"`
	// InstalledVersion is the version of the local copy, when there is one. Runes
	// carry no record of where they came from, so this is matched by id against
	// the repo being listed — which is right for the community catalog, and means
	// a rune from somewhere else simply reports nothing rather than a wrong answer.
	InstalledVersion int    `json:"installed_version,omitempty"`
	Builtin          bool   `json:"builtin,omitempty"` // ships in the binary; updates with the panel, not from here
	ParseError       string `json:"parse_error,omitempty"`
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

// ghHTTP issues a GitHub request. token (optional) is a personal access token,
// sent as a Bearer credential so PRIVATE rune repos can be listed and installed;
// without it only public repos are reachable. The header is only ever attached to
// GitHub's own hosts (ghAllowedHosts already gates callers), so the token can't
// leak to a third party via a crafted URL.
func ghHTTP(ctx context.Context, method, rawurl, accept, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawurl, nil)
	if err != nil {
		return nil, err
	}
	// GitHub rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "yggdrasil-rune-browser")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" && ghAllowedHosts[req.URL.Host] {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

// githubToken returns the stored (decrypted) GitHub PAT, or "" when none is set.
func (s *Server) githubToken(ctx context.Context) string {
	enc := s.getSetting(ctx, "github_token")
	if enc == "" {
		return ""
	}
	tok, err := s.cipher.Decrypt(enc)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(tok)
}

// effectiveGithubToken returns the credential to use for one repository: the
// token saved with that repo if it has one, otherwise the panel-wide token,
// otherwise none (public repos only).
//
// The panel holds a single GitHub token, which is enough right up until two
// private repos have different owners. A fine-grained token can only ever select
// repositories owned by the account that issued it, so reading a repo someone
// else owns means holding a token they issued — and with one slot, storing
// theirs takes away access to your own. A token per repo is the smallest thing
// that answers that; leave it empty and nothing changes.
//
// Matching is by (repo, path, ref) — the same triple the listing is fetched with —
// falling back to any saved row for the repo when only the folder or branch
// differs, since a credential grants access to a repository, not to a directory.
func (s *Server) effectiveGithubToken(ctx context.Context, repo, path, ref string) string {
	if repo != "" && s.cipher != nil {
		var enc string
		err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(token_enc,'') FROM rune_repos
			WHERE repo=? AND token_enc<>''
			ORDER BY (path=? AND COALESCE(ref,'main')=?) DESC, created_at
			LIMIT 1`, repo, path, ref).Scan(&enc)
		if err == nil && enc != "" {
			if tok, derr := s.cipher.Decrypt(enc); derr == nil {
				return strings.TrimSpace(tok)
			}
		}
	}
	return s.githubToken(ctx)
}

// ghTokenFingerprint is a short, non-reversible tag for the token in use. It goes
// into the listing cache key so that changing (or clearing) the token doesn't serve
// a cached listing the new credential shouldn't see.
func ghTokenFingerprint(token string) string {
	if token == "" {
		return "anon"
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:4])
}

// sanitizeGHDownloadURL strips a query string from a contents-API download_url.
// For a private repo GitHub embeds a short-lived `?token=…` there; keeping it would
// cache a credential, hand it to the browser, and break later installs when it
// expires. We drop it and authenticate with the PAT header instead.
func sanitizeGHDownloadURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	return u.String()
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

	token := s.effectiveGithubToken(r.Context(), repo, path, ref)
	cacheKey := repo + "|" + path + "|" + ref + "|" + ghTokenFingerprint(token)
	refresh := r.URL.Query().Get("refresh") == "1"

	ghRunesMu.Lock()
	cached, ok := ghRunesCache[cacheKey]
	ghRunesMu.Unlock()

	var runes []ghRune
	// Age of what is being served, so the UI can say so. The listing is cached
	// for ten minutes and nothing showed that: a rune updated in the catalogue
	// and then updated from the panel gave the OLD version back, twice in a row,
	// with the button appearing to work both times. The fetch is the expensive
	// part (GitHub's 60-req/hour budget), so the cache stays — it just stops
	// being invisible.
	fetchedAt := time.Now()
	fromCache := false
	if ok && !refresh && time.Since(cached.at) < 10*time.Minute {
		runes = cached.runes
		fetchedAt = cached.at
		fromCache = true
	} else {
		var err error
		runes, err = fetchGithubRunes(r.Context(), repo, path, ref, token)
		if err != nil {
			// Deliberately not 502: a proxy in front of the panel (Cloudflare
			// Tunnel, nginx) may swap a Bad Gateway body for its own error page,
			// and the message here — private repo, bad ref, rate limit — is the
			// whole point of the response. 400 reaches the browser intact.
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		ghRunesMu.Lock()
		ghRunesCache[cacheKey] = ghRunesEntry{at: time.Now(), runes: runes}
		ghRunesMu.Unlock()
	}

	// Match against what's installed — read fresh each call, never from the cache,
	// so an install or a delete shows up immediately.
	type local struct {
		version int
		builtin bool
	}
	installed := map[string]local{}
	if rows, err := s.db.QueryContext(r.Context(), "SELECT id, version, builtin FROM gameskills"); err == nil {
		for rows.Next() {
			var id string
			var l local
			var b int
			if rows.Scan(&id, &l.version, &b) == nil {
				l.builtin = b == 1
				installed[id] = l
			}
		}
		rows.Close()
	}
	out := make([]ghRune, len(runes))
	for i, g := range runes {
		if l, ok := installed[g.ID]; ok && g.ID != "" {
			g.Installed, g.InstalledVersion, g.Builtin = true, l.version, l.builtin
		}
		out[i] = g
	}

	jsonOK(w, map[string]any{
		"repo": repo, "path": path, "ref": ref, "runes": out,
		"fetched_at":    fetchedAt.UTC().Format(time.RFC3339),
		"from_cache":    fromCache,
		"cache_seconds": int(time.Since(fetchedAt).Seconds()),
	})
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
func ghListDir(ctx context.Context, repo, path, ref, token string) ([]ghDirEntry, error) {
	listURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s",
		repo, path, url.QueryEscape(ref))
	resp, err := ghHTTP(ctx, "GET", listURL, "application/vnd.github+json", token)
	if err != nil {
		return nil, fmt.Errorf("github unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		// GitHub answers 404 (not 403) for a private repo you can't see, so an
		// unauthenticated miss is most often a missing token rather than a typo.
		if token == "" {
			return nil, fmt.Errorf("not found: %s/%s@%s — if the repository is private, add a GitHub token in Settings → Integrations", repo, path, ref)
		}
		return nil, fmt.Errorf("not found: %s/%s@%s — check the repo, folder and branch (and that the token can read this repository)", repo, path, ref)
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("github rejected the token (401) — check the GitHub token in Settings → Integrations")
	case http.StatusForbidden:
		if token == "" {
			return nil, fmt.Errorf("github rate limit reached (60 requests/hour without a token) — add a GitHub token in Settings → Integrations, or try again later")
		}
		// 403 has two unrelated causes and opposite remedies: wait, or fix the
		// token. GitHub says which in the rate-limit header — remaining requests
		// left means it isn't a rate limit — so read it rather than making the
		// admin guess.
		//
		// When it is the token, a fine-grained one can fall short in two separate
		// ways, and naming only the first sends people to the wrong settings page.
		// The repository has to be in the token's scope, AND the token needs
		// Contents: Read — Metadata alone is enough to look a repository up but not
		// to read a file out of it, which is what a rune listing does. The second is
		// what actually happened here: a token set to "All repositories" with only
		// Metadata ticked, failing on the first private repo it was ever asked to
		// read while every public one had worked for weeks.
		if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
			reset := "shortly"
			if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
				if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
					reset = "at " + time.Unix(ts, 0).Format("15:04")
				}
			}
			return nil, fmt.Errorf("github rate limit reached — the quota resets %s; nothing is wrong with the token", reset)
		}
		return nil, fmt.Errorf("github refused the request (403) — this token can't read %s. On a fine-grained token check both: the repository must be in its Repository access, and Permissions must include Contents: Read-only (Metadata alone finds a repo but can't read files from it). Both apply to repositories you own. Otherwise give this repo its own token here", repo)
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
func fetchGithubRunes(ctx context.Context, repo, path, ref, token string) ([]ghRune, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	entries, err := ghListDir(ctx, repo, path, ref, token)
	if err != nil {
		return nil, err
	}
	var candidates []ghRune
	var subdirs []string
	for _, e := range entries {
		if e.Type == "file" && isYAMLName(e.Name) && e.DownloadURL != "" {
			candidates = append(candidates, ghRune{Filename: e.Name, DownloadURL: sanitizeGHDownloadURL(e.DownloadURL)})
		} else if e.Type == "dir" {
			subdirs = append(subdirs, path+"/"+e.Name)
		}
	}
	// Descend one level into subfolders (best-effort; a failed subdir is skipped).
	for _, sd := range subdirs {
		subEntries, serr := ghListDir(ctx, repo, sd, ref, token)
		if serr != nil {
			continue
		}
		for _, e := range subEntries {
			if e.Type == "file" && isYAMLName(e.Name) && e.DownloadURL != "" {
				candidates = append(candidates, ghRune{Filename: e.Name, DownloadURL: sanitizeGHDownloadURL(e.DownloadURL)})
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
			data, err := fetchGithubRaw(ctx, g.DownloadURL, token)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			gs, err := gameskill.Parse(data)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			g.ID, g.Name, g.Category, g.Description, g.Version = gs.ID, gs.Name, gs.Category, gs.Description, gs.Version
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

func fetchGithubRaw(ctx context.Context, rawurl, token string) ([]byte, error) {
	u, err := url.Parse(rawurl)
	if err != nil || u.Scheme != "https" || !ghAllowedHosts[u.Host] {
		return nil, fmt.Errorf("download url not allowed")
	}
	resp, err := ghHTTP(ctx, "GET", rawurl, "", token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && token == "" {
		return nil, fmt.Errorf("fetch returned 404 — if the repository is private, add a GitHub token in Settings → Integrations")
	}
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
	// The download URL is all we have here, so recover the repo from it to pick the
	// right credential — installing from a private repo must use the same token
	// that could list it, not the panel-wide one.
	srcRepo, srcPath, srcRef := parseRawGithubSource(req.DownloadURL)
	data, err := fetchGithubRaw(ctx, req.DownloadURL, s.effectiveGithubToken(r.Context(), srcRepo, srcPath, srcRef))
	if err != nil {
		// 400 rather than 502 for the same reason as the listing above: the
		// reason must survive any proxy between the panel and the browser.
		jsonError(w, "fetch: "+err.Error(), http.StatusBadRequest)
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
	// The source (parsed above) is recorded so the update check can compare against
	// its own repo — works for any repo, not just the default catalog.
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin, source_repo, source_path, source_ref)
		VALUES (?,?,?,?,?,0,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob,
			source_repo=excluded.source_repo, source_path=excluded.source_path, source_ref=excluded.source_ref
	`, gs.ID, gs.Name, gs.Category, gs.Version, string(data), srcRepo, srcPath, srcRef)
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
