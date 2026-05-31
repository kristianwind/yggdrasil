package gameskill

import "testing"

const sampleEgg = `{
  "name": "Paper",
  "author": "support@pterodactyl.io",
  "description": "High performance Minecraft server",
  "docker_images": { "Java 21": "ghcr.io/pterodactyl/yolks:java_21" },
  "startup": "java -Xms128M -jar {{SERVER_JARFILE}} nogui",
  "config": {
    "files": { "server.properties": { "parser": "properties" } },
    "startup": { "done": ")! For help, type " }
  },
  "scripts": {
    "installation": {
      "script": "#!/bin/bash\necho installing",
      "container": "debian:bullseye-slim",
      "entrypoint": "bash"
    }
  },
  "variables": [
    { "name": "Server Jar File", "env_variable": "SERVER_JARFILE", "default_value": "server.jar", "rules": "required|string|max:20" },
    { "name": "Build Type", "env_variable": "BUILD_TYPE", "default_value": "recommended", "rules": "required|in:recommended,latest" },
    { "name": "Max Memory", "env_variable": "MAX_MEMORY", "default_value": "2048", "rules": "required|integer" },
    { "name": "Enable Query", "env_variable": "QUERY", "default_value": "true", "rules": "required|boolean" }
  ]
}`

func TestImportEgg(t *testing.T) {
	gs, err := ImportEgg([]byte(sampleEgg))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if gs.ID != "paper" || gs.Name != "Paper" {
		t.Errorf("id/name: %q / %q", gs.ID, gs.Name)
	}
	if gs.Docker.Image != "ghcr.io/pterodactyl/yolks:java_21" {
		t.Errorf("image: %q", gs.Docker.Image)
	}
	if gs.Startup.Command == "" || gs.Startup.DoneRegex == "" {
		t.Errorf("startup not mapped: %+v", gs.Startup)
	}
	if gs.Install == nil || gs.Install.Image != "debian:bullseye-slim" {
		t.Errorf("install not mapped: %+v", gs.Install)
	}
	if len(gs.ConfigFiles) != 1 || gs.ConfigFiles[0] != "server.properties" {
		t.Errorf("config_files: %+v", gs.ConfigFiles)
	}
	if len(gs.Variables) != 4 {
		t.Fatalf("expected 4 variables, got %d", len(gs.Variables))
	}

	byKey := map[string]Variable{}
	for _, v := range gs.Variables {
		byKey[v.Key] = v
	}
	if byKey["BUILD_TYPE"].Type != "select" || len(byKey["BUILD_TYPE"].Options) != 2 {
		t.Errorf("BUILD_TYPE should be select with 2 options: %+v", byKey["BUILD_TYPE"])
	}
	if byKey["MAX_MEMORY"].Type != "int" {
		t.Errorf("MAX_MEMORY should be int: %+v", byKey["MAX_MEMORY"])
	}
	if byKey["QUERY"].Type != "bool" {
		t.Errorf("QUERY should be bool: %+v", byKey["QUERY"])
	}
	if byKey["SERVER_JARFILE"].Type != "string" {
		t.Errorf("SERVER_JARFILE should be string: %+v", byKey["SERVER_JARFILE"])
	}
}

func TestImportEggRejectsEmpty(t *testing.T) {
	if _, err := ImportEgg([]byte(`{}`)); err == nil {
		t.Error("expected error for nameless egg")
	}
}
