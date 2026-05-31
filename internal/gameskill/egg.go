package gameskill

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Pterodactyl "egg" JSON. We map the subset that translates cleanly to a
// gameskill so the large existing egg library can be reused.
type eggFile struct {
	Name        string          `json:"name"`
	Author      string          `json:"author"`
	Description string          `json:"description"`
	DockerImages map[string]string `json:"docker_images"`
	Image       string          `json:"image"`
	Startup     string          `json:"startup"`
	Config      eggConfig       `json:"config"`
	Scripts     eggScripts      `json:"scripts"`
	Variables   []eggVariable   `json:"variables"`
}

type eggConfig struct {
	Files   json.RawMessage `json:"files"`
	Startup json.RawMessage `json:"startup"` // {"done": "..."} or {"done": ["..."]}
}

type eggScripts struct {
	Installation struct {
		Script    string `json:"script"`
		Container string `json:"container"`
		Entry     string `json:"entrypoint"`
	} `json:"installation"`
}

type eggVariable struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	EnvVariable  string `json:"env_variable"`
	DefaultValue string `json:"default_value"`
	Rules        string `json:"rules"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ImportEgg converts a Pterodactyl egg JSON into a gameskill. The result is
// validated; callers can re-marshal it to YAML for storage/editing.
func ImportEgg(data []byte) (*Gameskill, error) {
	var egg eggFile
	if err := json.Unmarshal(data, &egg); err != nil {
		return nil, fmt.Errorf("egg parse: %w", err)
	}
	if egg.Name == "" {
		return nil, fmt.Errorf("egg has no name")
	}

	gs := &Gameskill{
		ID:          slugify(egg.Name),
		Name:        egg.Name,
		Category:    "Imported",
		Description: egg.Description,
		Author:      egg.Author,
		Version:     1,
		Startup:     Startup{Command: egg.Startup},
	}

	// Docker image: prefer the first of docker_images, else "image".
	gs.Docker.Image = egg.Image
	for _, img := range egg.DockerImages {
		gs.Docker.Image = img
		break
	}

	// done_regex from config.startup.done (string or [string]).
	if len(egg.Config.Startup) > 0 {
		var asObj struct {
			Done json.RawMessage `json:"done"`
		}
		if json.Unmarshal(egg.Config.Startup, &asObj) == nil && len(asObj.Done) > 0 {
			var s string
			if json.Unmarshal(asObj.Done, &s) == nil {
				gs.Startup.DoneRegex = regexp.QuoteMeta(s)
			} else {
				var arr []string
				if json.Unmarshal(asObj.Done, &arr) == nil && len(arr) > 0 {
					gs.Startup.DoneRegex = regexp.QuoteMeta(arr[0])
				}
			}
		}
	}

	// Install step.
	if egg.Scripts.Installation.Script != "" {
		img := egg.Scripts.Installation.Container
		if img == "" {
			img = gs.Docker.Image
		}
		gs.Install = &Install{Image: img, Script: egg.Scripts.Installation.Script}
	}

	// config_files from the keys of config.files.
	if len(egg.Config.Files) > 0 {
		var files map[string]json.RawMessage
		if json.Unmarshal(egg.Config.Files, &files) == nil {
			for name := range files {
				gs.ConfigFiles = append(gs.ConfigFiles, name)
			}
		}
	}

	// Variables.
	for _, ev := range egg.Variables {
		if ev.EnvVariable == "" {
			continue
		}
		v := Variable{
			Key:     ev.EnvVariable,
			Name:    ev.Name,
			Default: ev.DefaultValue,
		}
		v.Type, v.Options = inferType(ev.Rules)
		gs.Variables = append(gs.Variables, v)
	}

	if err := validate(gs); err != nil {
		return nil, fmt.Errorf("imported egg is not a valid gameskill: %w", err)
	}
	return gs, nil
}

// inferType derives a gameskill variable type (and options) from an egg's
// Laravel-style validation rules string, e.g. "required|string|max:20" or
// "required|in:vanilla,paper,forge".
func inferType(rules string) (string, []string) {
	parts := strings.Split(rules, "|")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case p == "boolean":
			return "bool", nil
		case p == "integer" || p == "numeric":
			return "int", nil
		case strings.HasPrefix(p, "in:"):
			opts := strings.Split(strings.TrimPrefix(p, "in:"), ",")
			for i := range opts {
				opts[i] = strings.TrimSpace(opts[i])
			}
			return "select", opts
		}
	}
	return "string", nil
}
