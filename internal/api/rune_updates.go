package api

import (
	"net/http"
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

	// Reuse the browser's listing and its 10-minute cache, so opening the Runes
	// page doesn't spend GitHub's rate limit.
	cacheKey := defaultRuneRepo + "|" + defaultRunePath + "|"
	ghRunesMu.Lock()
	cached, ok := ghRunesCache[cacheKey]
	ghRunesMu.Unlock()

	var runes []ghRune
	if ok && time.Since(cached.at) < 10*time.Minute {
		runes = cached.runes
	} else {
		var err error
		runes, err = fetchGithubRunes(r.Context(), defaultRuneRepo, defaultRunePath, "")
		if err != nil {
			out.Note = "Could not reach the rune catalog on GitHub: " + err.Error()
			jsonOK(w, out)
			return
		}
		ghRunesMu.Lock()
		ghRunesCache[cacheKey] = ghRunesEntry{at: time.Now(), runes: runes}
		ghRunesMu.Unlock()
	}

	// Installed, non-builtin runes and their versions.
	type local struct {
		name    string
		version int
	}
	installed := map[string]local{}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, version FROM gameskills WHERE builtin=0")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var id string
		var l local
		if rows.Scan(&id, &l.name, &l.version) == nil {
			installed[id] = l
		}
	}
	rows.Close()

	for _, g := range runes {
		if g.ID == "" || g.ParseError != "" {
			continue // a rune we couldn't read says nothing about the local copy
		}
		l, have := installed[g.ID]
		if !have || g.Version <= l.version {
			continue
		}
		name := l.name
		if name == "" {
			name = g.Name
		}
		out.Updates = append(out.Updates, runeUpdate{
			ID:               g.ID,
			Name:             name,
			InstalledVersion: l.version,
			AvailableVersion: g.Version,
			DownloadURL:      g.DownloadURL,
		})
	}
	jsonOK(w, out)
}
