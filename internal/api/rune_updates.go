package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Rune updates: which installed runes are behind the community catalog.
//
// The Browse GitHub modal already answers this if you go looking, one rune at a
// time. This answers it without being asked — the version a rune declares is the
// only signal that a catalog copy has drifted from its source, and until now
// nothing compared the two.
//
// Runes carry no record of where they were installed from, so this matches by id
// against the default community catalog. That's right for the runes that come
// from there, and a rune from anywhere else simply isn't reported rather than
// reported wrongly.
//
// Builtin runes are excluded: they're embedded in the binary and re-seeded on
// every boot, so they move with the panel and there is nothing to do here.

type runeUpdate struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	InstalledVersion int    `json:"installed_version"`
	AvailableVersion int    `json:"available_version"`
	DownloadURL      string `json:"download_url"`
}

type runeUpdatesResponse struct {
	Updates   []runeUpdate `json:"updates"`
	CheckedAt string       `json:"checked_at"`
	// Note carries a reason when the check couldn't run. An empty list because
	// GitHub was unreachable must not read as "everything is current".
	Note string `json:"note,omitempty"`
}

// handleRuneUpdates lists installed community runes with a newer version in the
// catalog. Admin-only, matching the rest of rune management: acting on this
// replaces a rune definition, which decides how servers are built.
func (s *Server) handleRuneUpdates(w http.ResponseWriter, r *http.Request) {
	out := runeUpdatesResponse{
		Updates:   []runeUpdate{},
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Installed, non-builtin runes, each with the source it came from. Runes without
	// a recorded source (uploaded by hand) fall back to the default catalog by id,
	// which is right for the ones that actually came from there.
	type local struct {
		name                  string
		version               int
		host, repo, path, ref string
	}
	installed := map[string]local{}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, version, COALESCE(source_host,''), COALESCE(source_repo,''), COALESCE(source_path,''), COALESCE(source_ref,'') FROM gameskills WHERE builtin=0")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var id string
		var l local
		if rows.Scan(&id, &l.name, &l.version, &l.host, &l.repo, &l.path, &l.ref) == nil {
			installed[id] = l
		}
	}
	rows.Close()

	// Group installed runes by the source they should be checked against, so each
	// distinct repo is fetched once. Empty source → the default catalog.
	type src struct{ host, repo, path, ref string }
	bysource := map[src][]string{} // source → rune ids
	for id, l := range installed {
		key := src{l.host, l.repo, l.path, l.ref}
		if key.repo == "" {
			key = src{"", defaultRuneRepo, defaultRunePath, ""}
		}
		bysource[key] = append(bysource[key], id)
	}

	var notes []string
	for source, ids := range bysource {
		runes, err := s.cachedRunes(r.Context(), source.host, source.repo, source.path, source.ref)
		if err != nil {
			name := source.repo
			if source.host != "" {
				name = source.host + "/" + source.repo
			}
			notes = append(notes, name+": "+err.Error())
			continue
		}
		repoVer := map[string]ghRune{}
		for _, g := range runes {
			if g.ID != "" && g.ParseError == "" {
				repoVer[g.ID] = g
			}
		}
		for _, id := range ids {
			l := installed[id]
			g, ok := repoVer[id]
			if !ok || g.Version <= l.version {
				continue
			}
			name := l.name
			if name == "" {
				name = g.Name
			}
			out.Updates = append(out.Updates, runeUpdate{
				ID:               id,
				Name:             name,
				InstalledVersion: l.version,
				AvailableVersion: g.Version,
				DownloadURL:      g.DownloadURL,
			})
		}
	}
	if len(notes) > 0 {
		out.Note = "Some sources couldn't be reached: " + strings.Join(notes, "; ")
	}
	jsonOK(w, out)
}

// cachedGithubRunes lists a GitHub repo's runes, reusing the browser's 10-minute cache.
func (s *Server) cachedGithubRunes(ctx context.Context, repo, path, ref string) ([]ghRune, error) {
	return s.cachedRunes(ctx, "", repo, path, ref)
}

// cachedRunes lists a repo's runes on any forge, reusing the browser's cache.
// A host no saved repository names any more is not contacted: the rune keeps
// its record, and the update check says the source is gone.
func (s *Server) cachedRunes(ctx context.Context, host, repo, path, ref string) ([]ghRune, error) {
	if !s.forgeHostKnown(ctx, host) {
		return nil, fmt.Errorf("no saved repository names %s any more", host)
	}
	token := s.effectiveRuneToken(ctx, host, repo, path, ref)
	cacheKey := host + "|" + repo + "|" + path + "|" + ref + "|" + ghTokenFingerprint(token)
	ghRunesMu.Lock()
	cached, ok := ghRunesCache[cacheKey]
	ghRunesMu.Unlock()
	if ok && time.Since(cached.at) < 10*time.Minute {
		return cached.runes, nil
	}
	runes, err := fetchRunes(ctx, host, repo, path, ref, token)
	if err != nil {
		return nil, err
	}
	ghRunesMu.Lock()
	ghRunesCache[cacheKey] = ghRunesEntry{at: time.Now(), runes: runes}
	ghRunesMu.Unlock()
	return runes, nil
}
