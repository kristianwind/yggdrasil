package api

import (
	"net/http"

	"github.com/kristianwind/yggdrasil/internal/compose"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// handleComposeImport translates an uploaded docker-compose file into a rune and
// stores it, so an operator can bring a plain docker-compose app into the panel
// without hand-writing a rune. Admin-only (a rune fully controls the container
// runtime). The caller then creates a server from the returned rune id the normal
// way. Returns { id, name, warnings } — warnings list bind mounts to re-add as
// host mounts and anything the translation dropped.
func (s *Server) handleComposeImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compose string `json:"compose"`
		Name    string `json:"name"`
		Main    string `json:"main"` // optional: which service is the primary app
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		jsonError(w, "a name is required", http.StatusBadRequest)
		return
	}

	f, err := compose.Parse([]byte(req.Compose))
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	gs, warnings, err := compose.Translate(f, req.Main, req.Name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Round-trip through the rune parser/validator so we never store an invalid
	// rune (and so the stored blob is exactly what the rest of the panel reads).
	blob, err := gameskill.ToYAML(gs)
	if err != nil {
		jsonError(w, "encode rune: "+err.Error(), http.StatusInternalServerError)
		return
	}
	parsed, err := gameskill.Parse(blob)
	if err != nil {
		jsonError(w, "the compose file could not be turned into a valid rune: "+err.Error(), http.StatusBadRequest)
		return
	}

	if s.isBuiltinRune(r.Context(), parsed.ID) {
		jsonError(w, "a built-in rune already uses id "+parsed.ID+"; pick a different name", http.StatusConflict)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
		VALUES (?,?,?,?,?,0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob
	`, parsed.ID, parsed.Name, parsed.Category, parsed.Version, string(blob)); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditLog(r, "gameskill.compose_import", "gameskill:"+parsed.ID, map[string]string{"name": parsed.Name})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"id": parsed.ID, "name": parsed.Name, "warnings": warnings})
}
