package gameskill

import (
	"encoding/xml"
	"fmt"
)

// XML import maps a gameskill expressed in XML (elements mirroring the YAML keys)
// onto the gameskill model. Example:
//
//	<gameskill>
//	  <id>foo</id><name>Foo</name>
//	  <docker><image>alpine</image></docker>
//	  <startup><command>./run</command></startup>
//	  <variables>
//	    <variable><key>K</key><name>K</name><type>select</type>
//	      <options><option>a</option><option>b</option></options>
//	      <default>a</default></variable>
//	  </variables>
//	  <ports><port><name>game</name><default>80</default><protocol>tcp</protocol></port></ports>
//	</gameskill>
type xmlGameskill struct {
	XMLName     xml.Name `xml:"gameskill"`
	ID          string   `xml:"id"`
	Name        string   `xml:"name"`
	Category    string   `xml:"category"`
	Description string   `xml:"description"`
	Author      string   `xml:"author"`
	Version     int      `xml:"version"`
	Icon        string   `xml:"icon"`
	Docker      struct {
		Image string `xml:"image"`
	} `xml:"docker"`
	Variables struct {
		Variable []xmlVariable `xml:"variable"`
	} `xml:"variables"`
	Install *struct {
		Image  string `xml:"image"`
		Script string `xml:"script"`
	} `xml:"install"`
	Startup struct {
		Command   string `xml:"command"`
		DoneRegex string `xml:"done_regex"`
		Stop      string `xml:"stop"`
	} `xml:"startup"`
	ConfigFiles struct {
		File []string `xml:"file"`
	} `xml:"config_files"`
	Ports struct {
		Port []xmlPort `xml:"port"`
	} `xml:"ports"`
}

type xmlVariable struct {
	Key      string `xml:"key"`
	Name     string `xml:"name"`
	Type     string `xml:"type"`
	Options  struct {
		Option []string `xml:"option"`
	} `xml:"options"`
	Default  string `xml:"default"`
	Required bool   `xml:"required"`
}

type xmlPort struct {
	Name     string `xml:"name"`
	Default  int    `xml:"default"`
	Protocol string `xml:"protocol"`
}

// ImportXML converts an XML gameskill document into a validated Gameskill.
func ImportXML(data []byte) (*Gameskill, error) {
	var x xmlGameskill
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}
	gs := &Gameskill{
		ID:          x.ID,
		Name:        x.Name,
		Category:    x.Category,
		Description: x.Description,
		Author:      x.Author,
		Version:     x.Version,
		Icon:        x.Icon,
		ConfigFiles: x.ConfigFiles.File,
	}
	if gs.Version == 0 {
		gs.Version = 1
	}
	gs.Docker.Image = x.Docker.Image
	gs.Startup = Startup{Command: x.Startup.Command, DoneRegex: x.Startup.DoneRegex, Stop: x.Startup.Stop}
	if x.Install != nil {
		gs.Install = &Install{Image: x.Install.Image, Script: x.Install.Script}
	}
	for _, v := range x.Variables.Variable {
		gs.Variables = append(gs.Variables, Variable{
			Key: v.Key, Name: v.Name, Type: v.Type,
			Options: v.Options.Option, Default: v.Default, Required: v.Required,
		})
	}
	for _, p := range x.Ports.Port {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		gs.Ports = append(gs.Ports, Port{Name: p.Name, Default: p.Default, Protocol: proto})
	}
	if err := validate(gs); err != nil {
		return nil, fmt.Errorf("imported XML is not a valid gameskill: %w", err)
	}
	return gs, nil
}
