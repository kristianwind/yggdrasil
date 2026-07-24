package compose

import "testing"

const sample = `
services:
  app:
    image: ghcr.io/example/app:latest
    ports:
      - "8080:3000"
      - "9000:9000/udp"
    environment:
      APP_SECRET: s3cret
      DB_HOST: db
    volumes:
      - appdata:/var/lib/app
      - ./config:/etc/app
    depends_on:
      - db
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=pw
    volumes:
      - dbdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"
  redis:
    image: redis:7
volumes:
  appdata:
  dbdata:
`

func TestTranslate(t *testing.T) {
	f, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	gs, warnings, err := Translate(f, "", "My App")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if gs.ID != "my-app" || gs.Name != "My App" {
		t.Errorf("id/name: got %q / %q", gs.ID, gs.Name)
	}
	if gs.Docker.Image != "ghcr.io/example/app:latest" {
		t.Errorf("main image wrong: %q", gs.Docker.Image)
	}
	// Two services with ports (app:3000 web-ish via 8080? no — container 3000).
	// app publishes 3000 (web-ish) and db 5432 → inferMain should pick "app".
	if gs.Docker.Env["APP_SECRET"] != "s3cret" || gs.Docker.Env["DB_HOST"] != "db" {
		t.Errorf("main env wrong: %v", gs.Docker.Env)
	}
	// Ports: 8080:3000 tcp (web) + 9000/udp.
	if len(gs.Ports) != 2 {
		t.Fatalf("want 2 ports, got %d: %+v", len(gs.Ports), gs.Ports)
	}
	if gs.Ports[0].Name != "web" || gs.Ports[0].Default != 8080 || gs.Ports[0].Protocol != "tcp" {
		t.Errorf("port0 wrong: %+v", gs.Ports[0])
	}
	if gs.Ports[1].Protocol != "udp" || gs.Ports[1].Default != 9000 {
		t.Errorf("port1 wrong: %+v", gs.Ports[1])
	}
	// Named volume → data_path; bind mount → warning.
	if gs.Docker.DataPath != "/var/lib/app" {
		t.Errorf("data_path wrong: %q", gs.Docker.DataPath)
	}
	foundBindWarn := false
	for _, w := range warnings {
		if contains(w, "bind mount") && contains(w, "/etc/app") {
			foundBindWarn = true
		}
	}
	if !foundBindWarn {
		t.Errorf("expected a bind-mount warning, got %v", warnings)
	}
	// Sidecars: db + redis (not app).
	if len(gs.Services) != 2 {
		t.Fatalf("want 2 sidecars, got %d: %+v", len(gs.Services), gs.Services)
	}
	var db *string
	for i := range gs.Services {
		if gs.Services[i].Name == "db" {
			db = &gs.Services[i].Image
			if gs.Services[i].DataPath != "/var/lib/postgresql/data" {
				t.Errorf("db data_path wrong: %q", gs.Services[i].DataPath)
			}
		}
	}
	if db == nil || *db != "postgres:16" {
		t.Errorf("db sidecar missing/wrong")
	}
}

func TestInferMainSinglePorted(t *testing.T) {
	f, _ := Parse([]byte(`
services:
  worker:
    image: worker:1
  web:
    image: web:1
    ports: ["80:80"]
`))
	gs, _, err := Translate(f, "", "x")
	if err != nil {
		t.Fatal(err)
	}
	if gs.Docker.Image != "web:1" {
		t.Errorf("inferMain should pick the only ported service, got %q", gs.Docker.Image)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
