package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

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
			if minLife < 0 || n < minLife {
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
			if n < floor {
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
