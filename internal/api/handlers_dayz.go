package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Norn — DayZ loot-economy helper. DayZ controls how long dropped items persist
// via each item's <lifetime> in types.xml; modded items frequently despawn too
// fast because their lifetime is low or they inherit the short
// globals.xml/CleanupLifetimeDefault. Norn reads those mission files and offers a
// one-click "minimum lifetime" floor across every registered types.xml (vanilla +
// modded), plus friendly editing of the globals cleanup timers.

var (
	dzLifetimeRe   = regexp.MustCompile(`<lifetime>\s*(\d+)\s*</lifetime>`)
	dzTypeNameRe   = regexp.MustCompile(`<type\s+name=`)
	dzGlobalVarRe  = regexp.MustCompile(`<var\s+name="(Cleanup\w+)"[^>]*\bvalue="(\d+)"`)
	dzCeBlockRe    = regexp.MustCompile(`(?is)<ce\s+folder="([^"]+)"\s*>(.*?)</ce>`)
	dzCeTypesFile  = regexp.MustCompile(`(?i)<file\s+name="([^"]+)"\s+type="types"\s*/>`)
)

// dayzMission resolves a DayZ server's data dir + mission name (empty ok=false if
// the server isn't a DayZ rune).
func (s *Server) dayzMission(ctx context.Context, id string) (dataDir, mission string, ok bool) {
	var gameskillID, envJSON string
	if err := s.db.QueryRowContext(ctx,
		"SELECT gameskill_id, env_json, data_dir FROM servers WHERE id=?", id).
		Scan(&gameskillID, &envJSON, &dataDir); err != nil || gameskillID != "dayz" {
		return "", "", false
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env)
	mission = env["MISSION"]
	if mission == "" {
		mission = "dayzOffline.chernarusplus"
	}
	return dataDir, mission, true
}

func dayzMissionDir(dataDir, mission string) string {
	return filepath.Join(dataDir, "mpmissions", mission)
}

// dayzTypesFiles returns the economy types.xml files for a mission: the default
// db/types.xml plus any registered via cfgeconomycore.xml (where modded loot is
// added). Only existing files are returned.
func dayzTypesFiles(missionDir string) []string {
	var files []string
	if def := filepath.Join(missionDir, "db", "types.xml"); fileExists(def) {
		files = append(files, def)
	}
	for _, ce := range []string{"cfgeconomycore.xml", "cfgEconomyCore.xml"} {
		data, err := os.ReadFile(filepath.Join(missionDir, ce))
		if err != nil {
			continue
		}
		for _, blk := range dzCeBlockRe.FindAllStringSubmatch(string(data), -1) {
			folder := blk[1]
			for _, fm := range dzCeTypesFile.FindAllStringSubmatch(blk[2], -1) {
				if p := filepath.Join(missionDir, folder, fm[1]); fileExists(p) {
					files = append(files, p)
				}
			}
		}
		break
	}
	return files
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// isDayzTypesFile heuristically identifies a loot types.xml (root <types>, item
// entries with a lifetime) — distinct from cfgspawnabletypes.xml / events.xml.
func isDayzTypesFile(head string) bool {
	return strings.Contains(head, "<types>") &&
		strings.Contains(head, "<type name=") &&
		strings.Contains(head, "<lifetime>")
}

// dayzRegisteredRel is the set of types files already in the economy (default +
// cfgeconomycore-registered), as mission-relative slash paths.
func dayzRegisteredRel(missionDir string) map[string]bool {
	set := map[string]bool{}
	for _, f := range dayzTypesFiles(missionDir) {
		if rel, err := filepath.Rel(missionDir, f); err == nil {
			set[filepath.ToSlash(rel)] = true
		}
	}
	return set
}

// dayzScanUnregistered walks the mission for types.xml files that aren't the
// default and aren't registered in cfgeconomycore.xml — i.e. modded loot the
// economy is currently ignoring.
func dayzScanUnregistered(missionDir string) []string {
	reg := dayzRegisteredRel(missionDir)
	var found []string
	filepath.WalkDir(missionDir, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(strings.ToLower(d.Name()), "storage") {
				return filepath.SkipDir // persistence dirs, not config
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".xml") {
			return nil
		}
		rel := filepath.ToSlash(mustRel(missionDir, path))
		if rel == "db/types.xml" || reg[rel] {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		buf := make([]byte, 16384)
		n, _ := f.Read(buf)
		f.Close()
		if isDayzTypesFile(string(buf[:n])) {
			found = append(found, rel)
		}
		return nil
	})
	sort.Strings(found)
	return found
}

func mustRel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

// dayzRegisterCe adds <ce folder><file type="types"> entries to cfgeconomycore.xml,
// skipping ones already registered. Returns the number added.
func dayzRegisterCe(missionDir string, entries [][2]string) (int, error) {
	cePath := filepath.Join(missionDir, "cfgeconomycore.xml")
	if !fileExists(cePath) {
		cePath = filepath.Join(missionDir, "cfgEconomyCore.xml")
	}
	data, err := os.ReadFile(cePath)
	if err != nil {
		return 0, fmt.Errorf("cfgeconomycore.xml not found")
	}
	content := string(data)
	idx := strings.LastIndex(content, "</economycore>")
	if idx < 0 {
		return 0, fmt.Errorf("cfgeconomycore.xml malformed (no </economycore>)")
	}
	reg := dayzRegisteredRel(missionDir)
	var b strings.Builder
	added := 0
	for _, e := range entries {
		folder, file := e[0], e[1]
		if folder == "" || folder == "." || reg[folder+"/"+file] {
			continue
		}
		b.WriteString(fmt.Sprintf("\t<ce folder=%q>\n\t\t<file name=%q type=\"types\" />\n\t</ce>\n", folder, file))
		reg[folder+"/"+file] = true // dedupe within this batch too
		added++
	}
	if added == 0 {
		return 0, nil
	}
	content = content[:idx] + b.String() + content[idx:]
	return added, os.WriteFile(cePath, []byte(content), 0o664)
}

// dayzModName reads a workshop mod's display name from its meta.cpp (name="…"),
// falling back to the folder name.
var dzMetaNameRe = regexp.MustCompile(`(?i)\bname\s*=\s*"([^"]+)"`)

func dayzModName(modDir, fallback string) string {
	data, err := os.ReadFile(filepath.Join(modDir, "meta.cpp"))
	if err == nil {
		if m := dzMetaNameRe.FindStringSubmatch(string(data)); m != nil {
			return m[1]
		}
	}
	return fallback
}

// handleDayzModLoot lists installed mods (the @<id> folders) and the loot
// types.xml files each ships — so an admin can pull modded loot into the economy.
func (s *Server) handleDayzModLoot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	mdir := dayzMissionDir(dataDir, mission)
	// Basenames already in the mission economy → mark mod files as "imported".
	importedBase := map[string]bool{}
	for rel := range dayzRegisteredRel(mdir) {
		importedBase[strings.ToLower(filepath.Base(rel))] = true
	}

	entries, _ := os.ReadDir(dataDir)
	type modFile struct {
		Path     string `json:"path"`
		Items    int    `json:"items"`
		Imported bool   `json:"imported"`
	}
	type modInfo struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Expansion bool      `json:"expansion"`
		Files     []modFile `json:"files"`
	}
	var mods []modInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "@") {
			continue
		}
		modDir := filepath.Join(dataDir, e.Name())
		name := dayzModName(modDir, e.Name())
		var files []modFile
		filepath.WalkDir(modDir, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".xml") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			buf := make([]byte, 16384)
			n, _ := f.Read(buf)
			f.Close()
			head := string(buf[:n])
			if !isDayzTypesFile(head) {
				return nil
			}
			// item count over the whole file
			full, _ := os.ReadFile(path)
			files = append(files, modFile{
				Path:     filepath.ToSlash(mustRel(dataDir, path)),
				Items:    len(dzTypeNameRe.FindAllString(string(full), -1)),
				Imported: importedBase[strings.ToLower(d.Name())],
			})
			return nil
		})
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		mods = append(mods, modInfo{
			ID:        strings.TrimPrefix(e.Name(), "@"),
			Name:      name,
			Expansion: strings.Contains(strings.ToLower(name+" "+e.Name()), "expansion"),
			Files:     files,
		})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Name < mods[j].Name })
	jsonOK(w, map[string]any{"mods": mods})
}

// handleDayzImportModTypes copies a mod's types.xml into the mission and registers
// it in the economy, so its loot spawns and is covered by the lifetime floor.
func (s *Server) handleDayzImportModTypes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Path == "" {
		jsonError(w, "path required", http.StatusBadRequest)
		return
	}
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	// Resolve + contain within the data dir (no traversal), and require @<mod>/… .
	src := filepath.Clean(filepath.Join(dataDir, req.Path))
	if !strings.HasPrefix(src, filepath.Clean(dataDir)+string(os.PathSeparator)) || !fileExists(src) {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	rel := filepath.ToSlash(mustRel(dataDir, src))
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "@") {
		jsonError(w, "not a mod file", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(src)
	if err != nil || !isDayzTypesFile(string(data)) {
		jsonError(w, "not a types.xml file", http.StatusBadRequest)
		return
	}
	slug := dayzSlug(strings.TrimPrefix(parts[0], "@"))
	base := filepath.Base(src)
	mdir := dayzMissionDir(dataDir, mission)
	destDir := filepath.Join(mdir, slug)
	if err := os.MkdirAll(destDir, 0o775); err != nil {
		jsonError(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(filepath.Join(destDir, base), data, 0o664); err != nil {
		jsonError(w, "copy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	added, err := dayzRegisterCe(mdir, [][2]string{{slug, base}})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "dayz.import_mod_types", "server:"+id, map[string]string{"file": rel})
	jsonOK(w, map[string]any{"imported": slug + "/" + base, "registered": added})
}

// dayzSlug makes a mod name safe for a mission subfolder.
func dayzSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "mod"
	}
	return out
}

// handleDayzRegisterTypes adds <ce> entries to cfgeconomycore.xml for every
// detected-but-unregistered types file in a subfolder, so modded loot spawns and
// is managed by the central economy (and covered by the lifetime floor).
func (s *Server) handleDayzRegisterTypes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	mdir := dayzMissionDir(dataDir, mission)
	var reg []string
	for _, rel := range dayzScanUnregistered(mdir) {
		if filepath.Dir(rel) != "." { // need a subfolder for a valid <ce folder="...">
			reg = append(reg, rel)
		}
	}
	if len(reg) == 0 {
		jsonOK(w, map[string]any{"registered": 0, "files": []string{}})
		return
	}
	entries := make([][2]string, 0, len(reg))
	for _, rel := range reg {
		entries = append(entries, [2]string{filepath.ToSlash(filepath.Dir(rel)), filepath.Base(rel)})
	}
	added, err := dayzRegisterCe(mdir, entries)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "dayz.register_types", "server:"+id, map[string]string{"count": strconv.Itoa(added)})
	jsonOK(w, map[string]any{"registered": added, "files": reg})
}

// handleDayzEconomy returns a summary of the loot economy: types files, item +
// lifetime stats, and the globals cleanup timers.
func (s *Server) handleDayzEconomy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	mdir := dayzMissionDir(dataDir, mission)
	files := dayzTypesFiles(mdir)

	type fileSum struct {
		Path        string `json:"path"`
		Items       int    `json:"items"`
		MinLifetime int    `json:"min_lifetime"`
		Modded      bool   `json:"modded"`
	}
	var sums []fileSum
	totalItems, overallMin := 0, -1
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		str := string(data)
		items := len(dzTypeNameRe.FindAllString(str, -1))
		minLife := -1
		for _, m := range dzLifetimeRe.FindAllStringSubmatch(str, -1) {
			n, _ := strconv.Atoi(m[1])
			// 0 means "unmanaged" in DayZ (special/non-spawning entries) — ignore it
			// so the reported minimum reflects real loot.
			if n > 0 && (minLife < 0 || n < minLife) {
				minLife = n
			}
		}
		rel, _ := filepath.Rel(mdir, f)
		sums = append(sums, fileSum{Path: rel, Items: items, MinLifetime: minLife, Modded: rel != filepath.Join("db", "types.xml")})
		totalItems += items
		if minLife >= 0 && (overallMin < 0 || minLife < overallMin) {
			overallMin = minLife
		}
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].Path < sums[j].Path })

	jsonOK(w, map[string]any{
		"mission":      mission,
		"found":        len(files) > 0,
		"files":        sums,
		"total_items":  totalItems,
		"min_lifetime": overallMin,
		"globals":      dayzReadGlobals(filepath.Join(mdir, "db", "globals.xml")),
		"unregistered": dayzScanUnregistered(mdir),
	})
}

func dayzReadGlobals(path string) map[string]int {
	out := map[string]int{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, g := range dzGlobalVarRe.FindAllStringSubmatch(string(data), -1) {
		v, _ := strconv.Atoi(g[2])
		out[g[1]] = v
	}
	return out
}

// handleDayzMinLifetime raises every item <lifetime> below the given floor (hours)
// up to it, across all types files — so nothing despawns faster than that. Only
// edits the numbers, preserving the rest of the file (comments, formatting).
func (s *Server) handleDayzMinLifetime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		Hours float64 `json:"hours"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Hours <= 0 {
		jsonError(w, "hours must be > 0", http.StatusBadRequest)
		return
	}
	floor := int(req.Hours * 3600)
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	mdir := dayzMissionDir(dataDir, mission)
	changed, filesChanged := 0, 0
	for _, f := range dayzTypesFiles(mdir) {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		c := 0
		out := dzLifetimeRe.ReplaceAllStringFunc(string(data), func(m string) string {
			n, _ := strconv.Atoi(dzLifetimeRe.FindStringSubmatch(m)[1])
			// Skip 0 (unmanaged/special entries); only raise real, too-short lifetimes.
			if n > 0 && n < floor {
				c++
				return "<lifetime>" + strconv.Itoa(floor) + "</lifetime>"
			}
			return m
		})
		if c > 0 {
			if err := os.WriteFile(f, []byte(out), 0o664); err == nil {
				changed += c
				filesChanged++
			}
		}
	}
	s.auditLog(r, "dayz.min_lifetime", "server:"+id, map[string]string{
		"hours": strconv.FormatFloat(req.Hours, 'g', -1, 64), "changed": strconv.Itoa(changed),
	})
	jsonOK(w, map[string]any{"changed": changed, "files": filesChanged, "floor_seconds": floor})
}

// handleDayzGlobals updates allowlisted globals.xml cleanup timers (seconds).
func (s *Server) handleDayzGlobals(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var req map[string]int
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	allowed := map[string]bool{
		"CleanupLifetimeDefault": true, "CleanupLifetimeRuined": true,
		"CleanupLifetimeDeployed": true, "CleanupLifetimeDeadPlayer": true,
		"CleanupLifetimeLimit": true, "CleanupLifetimeDeadAnimal": true,
		"CleanupLifetimeDeadInfected": true,
	}
	dataDir, mission, ok := s.dayzMission(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	path := filepath.Join(dayzMissionDir(dataDir, mission), "db", "globals.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		jsonError(w, "globals.xml not found", http.StatusNotFound)
		return
	}
	out, changed := string(data), 0
	for name, val := range req {
		if !allowed[name] || val < 0 {
			continue
		}
		re := regexp.MustCompile(`(<var\s+name="` + regexp.QuoteMeta(name) + `"[^>]*\bvalue=")\d+(")`)
		if nv := re.ReplaceAllString(out, "${1}"+strconv.Itoa(val)+"${2}"); nv != out {
			out, changed = nv, changed+1
		}
	}
	if changed > 0 {
		os.WriteFile(path, []byte(out), 0o664) //nolint:errcheck
	}
	s.auditLog(r, "dayz.globals", "server:"+id, nil)
	jsonOK(w, map[string]any{"changed": changed})
}
