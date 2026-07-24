// Package compose translates a docker-compose file into a Yggdrasil rune
// (gameskill) so an operator can bring a plain docker-compose app into the panel
// without hand-writing a rune. It is a best-effort MVP: it covers the common
// shape (one main service + supporting sidecars, ports, environment, named
// volumes) and returns warnings for anything a rune can't express (bind mounts
// become admin host mounts; build:, networks:, and the like are ignored).
package compose

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"gopkg.in/yaml.v3"
)

// File is the subset of docker-compose we read.
type File struct {
	Services map[string]Service `yaml:"services"`
}

// Service is one compose service. Environment accepts both the map and the list
// form; Ports/Volumes are the short string syntax (the long object syntax is
// tolerated only where noted).
type Service struct {
	Image       string    `yaml:"image"`
	Ports       []string  `yaml:"ports"`
	Environment EnvMap    `yaml:"environment"`
	Volumes     []string  `yaml:"volumes"`
	Command     yaml.Node `yaml:"command"`
	Restart     string    `yaml:"restart"`
	DependsOn   yaml.Node `yaml:"depends_on"`
}

// EnvMap unmarshals compose's two environment shapes into an ordered key list.
type EnvMap struct {
	Keys   []string
	Values map[string]string
}

func (e *EnvMap) UnmarshalYAML(n *yaml.Node) error {
	e.Values = map[string]string{}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			e.Keys = append(e.Keys, k)
			e.Values[k] = n.Content[i+1].Value
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			kv := item.Value
			k, v, _ := strings.Cut(kv, "=")
			e.Keys = append(e.Keys, k)
			e.Values[k] = v
		}
	}
	return nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "app"
	}
	return s
}

// parsePort turns a compose ports entry ("8080:80", "80", "1.2.3.4:8080:80/udp")
// into a host port (0 if unspecified), container port and protocol.
func parsePort(s string) (host, container int, proto string, ok bool) {
	proto = "tcp"
	if i := strings.Index(s, "/"); i >= 0 {
		proto = strings.ToLower(s[i+1:])
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	// last = container, second-last (if any) = host, anything before = bind IP.
	cs := parts[len(parts)-1]
	container, err := strconv.Atoi(strings.TrimSpace(cs))
	if err != nil {
		return 0, 0, "", false // ranges like 8000-8010 aren't supported
	}
	if len(parts) >= 2 {
		if h, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2])); err == nil {
			host = h
		}
	}
	return host, container, proto, true
}

// Translate converts a compose file to a rune. mainSvc names the primary service
// (the one whose ports the panel publishes); if empty, it is inferred. name is
// the human name for the rune/server. Returns the rune and warnings about
// anything dropped or needing manual setup (e.g. bind mounts).
func Translate(f *File, mainSvc, name string) (*gameskill.Gameskill, []string, error) {
	if len(f.Services) == 0 {
		return nil, nil, fmt.Errorf("no services in the compose file")
	}
	names := make([]string, 0, len(f.Services))
	for n := range f.Services {
		names = append(names, n)
	}
	sort.Strings(names) // stable, deterministic ordering (compose maps are unordered)

	if mainSvc == "" {
		mainSvc = inferMain(f, names)
	}
	if _, ok := f.Services[mainSvc]; !ok {
		return nil, nil, fmt.Errorf("main service %q not found (services: %s)", mainSvc, strings.Join(names, ", "))
	}

	var warnings []string
	main := f.Services[mainSvc]

	gs := &gameskill.Gameskill{
		ID:          slug(name),
		Name:        name,
		Category:    "Apps",
		Description: fmt.Sprintf("Imported from docker-compose (main service %q).", mainSvc),
		Author:      "compose-import",
		Version:     1,
		Icon:        "app",
		Docker: gameskill.Docker{
			Image:          main.Image,
			KeepEntrypoint: true, // run the image's own entrypoint, like a plain compose up
			Env:            main.Environment.Values,
		},
		Startup: gameskill.Startup{},
	}
	if len(main.Environment.Values) == 0 {
		gs.Docker.Env = nil
	}

	// Ports on the main service become the published rune ports.
	gs.Ports = translatePorts(main.Ports, mainSvc, &warnings)
	if len(gs.Ports) == 0 {
		warnings = append(warnings, "main service publishes no ports — the app will have no reachable address")
	}

	// Volumes → data_path / extra_volumes (named volumes) or host-mount warnings
	// (bind mounts, which are admin-only and per-server, never in a rune).
	dataPath, extra, mountWarn := translateVolumes(mainSvc, main.Volumes)
	gs.Docker.DataPath = dataPath
	gs.Docker.ExtraVolumes = extra
	warnings = append(warnings, mountWarn...)

	// Every other service becomes a sidecar reachable by its name on the stack net.
	for _, n := range names {
		if n == mainSvc {
			continue
		}
		svc := f.Services[n]
		sc := gameskill.Service{
			Name:  n,
			Image: svc.Image,
			Env:   svc.Environment.Values,
		}
		if len(svc.Environment.Values) == 0 {
			sc.Env = nil
		}
		dp, _, sw := translateVolumes(n, svc.Volumes)
		sc.DataPath = dp
		sc.Command = stringList(svc.Command)
		gs.Services = append(gs.Services, sc)
		for _, msg := range sw {
			warnings = append(warnings, "sidecar "+n+": "+msg)
		}
		if len(svc.Ports) > 0 {
			warnings = append(warnings, fmt.Sprintf("sidecar %q publishes ports in compose; sidecars are reached by service name and are not published (declare them on the main service if you need external access)", n))
		}
	}

	return gs, warnings, nil
}

// inferMain picks the primary service: prefer the sole service with published
// ports; else one publishing a common web port; else the first name.
func inferMain(f *File, names []string) string {
	var withPorts []string
	for _, n := range names {
		if len(f.Services[n].Ports) > 0 {
			withPorts = append(withPorts, n)
		}
	}
	if len(withPorts) == 1 {
		return withPorts[0]
	}
	web := map[int]bool{80: true, 443: true, 3000: true, 8080: true, 8000: true, 8443: true}
	for _, n := range withPorts {
		for _, p := range f.Services[n].Ports {
			if _, c, _, ok := parsePort(p); ok && web[c] {
				return n
			}
		}
	}
	if len(withPorts) > 0 {
		return withPorts[0]
	}
	return names[0]
}

func translatePorts(ports []string, svc string, warnings *[]string) []gameskill.Port {
	web := map[int]bool{80: true, 443: true, 3000: true, 8080: true, 8000: true, 8443: true}
	var out []gameskill.Port
	namedWeb := false
	for _, p := range ports {
		host, container, proto, ok := parsePort(p)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("skipped unsupported port spec %q (ranges aren't supported)", p))
			continue
		}
		def := host
		if def == 0 {
			def = container
		}
		// Name exactly one tcp port "web" (the HTTP UI, so the NPM/subdomain feature
		// can target it): prefer a well-known web container port, else the first tcp.
		name := fmt.Sprintf("%s-%d", svc, container)
		if !namedWeb && proto == "tcp" && web[container] {
			name, namedWeb = "web", true
		}
		out = append(out, gameskill.Port{Name: name, Default: def, Protocol: proto})
	}
	if !namedWeb { // no well-known web port — promote the first tcp port to "web"
		for i := range out {
			if out[i].Protocol == "tcp" {
				out[i].Name = "web"
				break
			}
		}
	}
	return out
}

// translateVolumes maps compose volumes to a rune: the first named-volume
// container path becomes data_path, further named volumes become extra_volumes,
// and bind mounts (host paths) become warnings — they must be added per-server
// as admin host mounts.
func translateVolumes(svc string, vols []string) (dataPath string, extra []string, warnings []string) {
	for _, v := range vols {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			continue // anonymous volume — the panel persists the data dir anyway
		}
		src, dst := parts[0], parts[1]
		if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~") {
			warnings = append(warnings, fmt.Sprintf("bind mount %q → add it as an admin host mount on the server (Settings → Host mounts) mapping your host path to %q", v, dst))
			continue
		}
		// named volume
		if dataPath == "" {
			dataPath = dst
		} else {
			extra = append(extra, dst)
		}
	}
	return dataPath, extra, warnings
}

// stringList reads a compose command (string or list form) into an argv slice.
func stringList(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return strings.Fields(n.Value)
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	}
	return nil
}

// Parse reads a compose file's bytes.
func Parse(b []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	return &f, nil
}
