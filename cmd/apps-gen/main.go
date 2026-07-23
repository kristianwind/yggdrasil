// apps-gen scans every rune (builtin + community), vendors each app's icon from
// the dashboard-icons collection into website/icons/, and generates the
// "Supported apps & games" showcase at website/apps.html. It reads the runes as the
// single source of truth, so adding a rune adds a card automatically: just re-run.
//
//	go run ./cmd/apps-gen
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

var runeDirs = []string{"builtin-runes", "community-runes"}

const (
	outHTML  = "website/apps/index.html" // served at the clean URL /apps
	iconsDir = "website/icons"
	iconBase = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons"
)

// iconSlug maps a rune id to its dashboard-icons name when they differ. Anything
// not here is tried as-is (its id).
var iconSlug = map[string]string{
	"pihole":          "pi-hole",
	"adguardhome":     "adguard-home",
	"minecraft-java":  "minecraft",
	"minecraft-bedrock": "minecraft",
	"static-site":     "", // no brand icon — falls back to a glyph
	"cyberchef":       "cyberchef",
	"it-tools":        "it-tools",
}

type app struct {
	ID, Name, Category, Description string
	Stack                          bool
	Icon                           string // vendored path, or "" for the glyph fallback
	IsApp                          bool
}

func main() {
	apps, err := collect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "apps-gen:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(iconsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "apps-gen:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(outHTML), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "apps-gen:", err)
		os.Exit(1)
	}
	for i := range apps {
		apps[i].Icon = vendorIcon(apps[i].ID)
	}
	if err := os.WriteFile(outHTML, []byte(render(apps)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "apps-gen:", err)
		os.Exit(1)
	}
	fmt.Printf("apps-gen: %d runes → %s\n", len(apps), outHTML)
}

func collect() ([]app, error) {
	seen := map[string]bool{}
	var out []app
	for _, dir := range runeDirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}
			if strings.Contains(path, "README") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			gs, perr := gameskill.Parse(data)
			if perr != nil || gs.ID == "" || seen[gs.ID] {
				return nil
			}
			seen[gs.ID] = true
			// Apps and databases carry a shared category ("Apps"/"Databases"); every
			// game rune has its own game name as the category (DayZ, Minecraft, …). A
			// game signal (query/steam/rcon) forces the game side as a backstop.
			cat := strings.ToLower(strings.TrimSpace(gs.Category))
			isGame := gs.Query != nil || gs.Steam != nil || gs.RCON != nil
			isApp := !isGame && (cat == "apps" || cat == "databases" || cat == "database" || cat == "")
			out = append(out, app{
				ID: gs.ID, Name: gs.Name, Category: gs.Category, Description: gs.Description,
				Stack: len(gs.Services) > 0, IsApp: isApp,
			})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// vendorIcon downloads a rune's icon into website/icons and returns its site path,
// or "" if none is available (the page uses a glyph instead).
func vendorIcon(id string) string {
	slug, ok := iconSlug[id]
	if !ok {
		slug = id
	}
	if slug == "" {
		return ""
	}
	dst := filepath.Join(iconsDir, id+".svg")
	if _, err := os.Stat(dst); err == nil {
		return "/icons/" + id + ".svg" // already vendored
	}
	for _, ext := range []string{"svg", "png", "webp"} {
		url := fmt.Sprintf("%s/%s/%s.%s", iconBase, ext, slug, ext)
		if body, ok := fetch(url); ok {
			name := id + "." + ext
			if os.WriteFile(filepath.Join(iconsDir, name), body, 0o644) == nil {
				fmt.Printf("  icon %s ← %s\n", id, slug)
				return "/icons/" + name
			}
		}
	}
	fmt.Printf("  (no icon for %s — glyph fallback)\n", id)
	return ""
}

func fetch(url string) ([]byte, bool) {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}
