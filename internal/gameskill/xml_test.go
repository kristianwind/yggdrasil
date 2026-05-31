package gameskill

import "testing"

const sampleXML = `<gameskill>
  <id>test-xml</id>
  <name>Test XML</name>
  <category>Imported</category>
  <docker><image>alpine:latest</image></docker>
  <startup><command>./run</command><done_regex>ready</done_regex></startup>
  <variables>
    <variable>
      <key>MODE</key><name>Mode</name><type>select</type>
      <options><option>a</option><option>b</option></options>
      <default>a</default>
    </variable>
    <variable><key>PORTNUM</key><name>Port</name><type>int</type><default>80</default></variable>
  </variables>
  <ports>
    <port><name>game</name><default>80</default><protocol>tcp</protocol></port>
  </ports>
</gameskill>`

func TestImportXML(t *testing.T) {
	gs, err := ImportXML([]byte(sampleXML))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if gs.ID != "test-xml" || gs.Docker.Image != "alpine:latest" {
		t.Errorf("basic fields: %+v", gs)
	}
	if gs.Startup.Command != "./run" || gs.Startup.DoneRegex != "ready" {
		t.Errorf("startup: %+v", gs.Startup)
	}
	if len(gs.Variables) != 2 {
		t.Fatalf("expected 2 variables, got %d", len(gs.Variables))
	}
	if gs.Variables[0].Type != "select" || len(gs.Variables[0].Options) != 2 {
		t.Errorf("select var: %+v", gs.Variables[0])
	}
	if len(gs.Ports) != 1 || gs.Ports[0].Default != 80 {
		t.Errorf("ports: %+v", gs.Ports)
	}
}

func TestImportXMLInvalid(t *testing.T) {
	// Missing required docker.image / startup.command.
	if _, err := ImportXML([]byte(`<gameskill><id>x</id><name>X</name></gameskill>`)); err == nil {
		t.Error("expected validation error")
	}
}
