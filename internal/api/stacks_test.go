package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// TestSidecarPortsReuse covers the reuse path (no Docker needed): an already
// allocated sidecar port is returned as-is and mirrored into ports_json, so the
// port stays stable across restarts and shows in the server's port list.
func TestSidecarPortsReuse(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir) VALUES ('srv','TM','teslamate','stopped','{}','{\"web\":25000}','/tmp/x')")
	// Pre-allocate grafana's port so sidecarPorts takes the reuse branch (no docker).
	s.db.Exec("INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (25012,'srv','tcp','grafana.web')")

	svc := gameskill.Service{Name: "grafana", Ports: []gameskill.Port{{Name: "web", Default: 3000, Protocol: "tcp"}}}
	got := s.sidecarPorts(ctx, "srv", svc)
	if len(got) != 1 || got[0].HostPort != 25012 || got[0].ContainerPort != 3000 {
		t.Fatalf("expected host 25012 -> container 3000, got %+v", got)
	}
	// ports_json now carries the sidecar port under its namespaced key.
	var pj string
	s.db.QueryRow("SELECT ports_json FROM servers WHERE id='srv'").Scan(&pj)
	m := map[string]int{}
	json.Unmarshal([]byte(pj), &m) //nolint:errcheck
	if m["grafana.web"] != 25012 || m["web"] != 25000 {
		t.Fatalf("ports_json not merged correctly: %v", m)
	}
}

func TestServiceWithPortsParses(t *testing.T) {
	// A sidecar declaring a published port must validate.
	yaml := `
gameskill:
  id: stacktest
  name: Stack Test
  category: app
  version: 1
  docker:
    image: nginx:alpine
  startup:
    command: "run"
  ports:
    - { name: web, default: 8080, protocol: tcp }
  services:
    - name: grafana
      image: grafana/grafana:latest
      data_path: /var/lib/grafana
      ports:
        - { name: web, default: 3000, protocol: tcp }
`
	gs, err := gameskill.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("service ports rejected: %v", err)
	}
	if len(gs.Services) != 1 || len(gs.Services[0].Ports) != 1 || gs.Services[0].Ports[0].Default != 3000 {
		t.Fatalf("service ports not parsed: %+v", gs.Services)
	}
}
