package gameskill

import (
	"strings"
	"testing"
)

const importBase = `
gameskill:
  id: app
  name: App
  category: app
  version: 1
  docker:
    image: nginx:alpine
  startup:
    command: "run"
  ports:
    - name: web
      default: 80
      protocol: tcp
`

func TestImportValidation(t *testing.T) {
	ok := importBase + `
  import:
    inputs:
      - key: files
      - key: db
    steps:
      - unpack: files
      - db_import:
          input: db
          service: db
          image: "mariadb:lts"
          command: "mariadb -h db -u u -pp d"
      - script: "echo hi"
`
	gs, err := Parse([]byte(ok))
	if err != nil {
		t.Fatalf("valid import rejected: %v", err)
	}
	if gs.Import == nil || len(gs.Import.Steps) != 3 {
		t.Fatalf("import not parsed: %+v", gs.Import)
	}

	cases := map[string]string{
		"no steps": `
  import:
    inputs: [{key: files}]`,
		"unknown unpack input": `
  import:
    inputs: [{key: files}]
    steps:
      - unpack: nope`,
		"two verbs in one step": `
  import:
    inputs: [{key: files}]
    steps:
      - unpack: files
        script: "echo"`,
		"db_import missing fields": `
  import:
    inputs: [{key: db}]
    steps:
      - db_import: {input: db, service: db}`,
		"unpack path traversal": `
  import:
    inputs: [{key: files}]
    steps:
      - unpack: files
        to: "../etc"`,
	}
	for name, frag := range cases {
		if _, err := Parse([]byte(importBase + frag)); err == nil {
			t.Errorf("%s: expected a validation error, got none", name)
		}
	}
}

func TestImportTemplating(t *testing.T) {
	// The db_import command templates {{VARS}} like everything else.
	got := ApplyTemplate("mariadb -h db -u {{DB_USER}} -p{{DB_PASSWORD}} {{DB_NAME}}",
		map[string]string{"DB_USER": "wp", "DB_PASSWORD": "s3cret", "DB_NAME": "wordpress"})
	if !strings.Contains(got, "-u wp ") || !strings.Contains(got, "-ps3cret ") || !strings.HasSuffix(got, "wordpress") {
		t.Fatalf("templating wrong: %q", got)
	}
}
