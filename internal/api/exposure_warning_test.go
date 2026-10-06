package api

import (
	"os"
	"strings"
	"testing"
)

// Every control that opens something to a network the operator does not own
// has to say so where the control is, not in the docs.
//
// This is a list rather than a pattern on purpose, and it is the same shape as
// publicRoutes in route_auth_test.go: a new exposure control is a deliberate
// act, so it should cost one line here with a reason, and the test should fail
// until somebody writes it. A heuristic that tried to RECOGNISE an exposure
// control would pass the day somebody adds one it has no pattern for -- which
// is exactly when the warning is missing.
var exposureControls = []struct {
	file   string // the view that carries the control
	anchor string // text unique to the control, as the operator sees it
	why    string // what it opens, for the reader of this test
}{
	{
		file:   "web/src/views/Settings.svelte",
		anchor: "ask the router (via UPnP-IGD) to forward its ports",
		why:    "UPnP: any port, no authentication anywhere in the request, the whole internet",
	},
	{
		file:   "web/src/views/Settings.svelte",
		anchor: "Automatically create/remove WAN port-forward rules on your UniFi gateway",
		why:    "UniFi: the same port opened to the same internet, with an audit trail",
	},
	{
		file:   "web/src/views/Settings.svelte",
		anchor: "Give HTTP app servers their own subdomain.",
		why:    "NPM: an app published on a hostname, and 80/443 open on the public IP",
	},
	{
		file:   "web/src/views/Settings.svelte",
		anchor: "Expose HTTP app servers on a subdomain through a Cloudflare Tunnel",
		why:    "Cloudflare Tunnel: published, but outbound-only -- the note says so rather than warns",
	},
	{
		file:   "web/src/views/ServerDetail.svelte",
		anchor: "Turn off to keep this server LAN-only",
		why:    "auto-forward, and it is ON for every new server",
	},
	{
		file:   "web/src/views/ServerDetail.svelte",
		anchor: "public <code>/status</code> page (no login)",
		why:    "the public status page: server names and who is playing, to anyone",
	},
}

// How far after the control's own text the note may sit. Generous enough for a
// help paragraph in between, tight enough that a note belonging to the NEXT
// control cannot be mistaken for this one's.
const exposureNoteWindow = 14

func TestEveryExposureControlWarns(t *testing.T) {
	if len(exposureControls) < 6 {
		t.Fatalf("only %d exposure controls listed -- this test is the kind that passes over "+
			"nothing, so it refuses to run on a list somebody emptied", len(exposureControls))
	}

	for _, c := range exposureControls {
		t.Run(c.why, func(t *testing.T) {
			b, err := os.ReadFile("../../" + c.file)
			if err != nil {
				t.Fatalf("read %s: %v", c.file, err)
			}
			lines := strings.Split(string(b), "\n")

			at := -1
			for i, l := range lines {
				if strings.Contains(l, c.anchor) {
					if at >= 0 {
						t.Fatalf("%q appears more than once in %s, so this test cannot say "+
							"which control it checked", c.anchor, c.file)
					}
					at = i
				}
			}
			if at < 0 {
				t.Fatalf("could not find %q in %s -- the control was renamed or removed, and a "+
					"check that cannot find its subject passes over nothing", c.anchor, c.file)
			}

			end := at + exposureNoteWindow
			if end > len(lines) {
				end = len(lines)
			}
			if !strings.Contains(strings.Join(lines[at:end], "\n"), "<ExposureNote") {
				t.Errorf("%s:%d opens something and carries no <ExposureNote> within %d lines.\n"+
					"It exposes: %s", c.file, at+1, exposureNoteWindow, c.why)
			}
		})
	}
}

// The note styles itself from a lookup rather than by building class names, and
// that is load-bearing: Tailwind emits a class only when it can SEE the literal
// string, and this project has no safelist. `border-{tone}` compiles to a note
// with no colour at all -- styling that silently does nothing, on the one
// component whose entire job is to be noticed, and nothing in a build or a test
// run would say so.
func TestExposureNoteUsesLiteralClassNames(t *testing.T) {
	b, err := os.ReadFile("../../web/src/components/ExposureNote.svelte")
	if err != nil {
		t.Fatalf("read the component: %v", err)
	}
	src := string(b)

	for _, want := range []string{
		"border-danger", "border-warn", "border-accent",
		"text-danger", "text-warn", "text-accent",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%q is not written out in full, so Tailwind will not generate it", want)
		}
	}

	// The failure this guards against is an interpolated class, e.g.
	// class="border-" followed by a brace. Catch the shape, not one instance.
	//
	// Comments are stripped first, and that is not tidiness: the component
	// documents this very mistake by naming it, so a scan over the raw file
	// fails on its own explanation -- the check would be red on correct code
	// and the next person would delete it rather than read it.
	code := stripSvelteComments(src)
	for _, bad := range []string{"border-{", "bg-{", "text-{"} {
		if strings.Contains(code, bad) {
			t.Errorf("found %q -- a class assembled at runtime is a class Tailwind never emits", bad)
		}
	}

	// ...and prove the stripping did not simply eat the file, which would make
	// the loop above a scan over nothing.
	if !strings.Contains(code, "ExposureNote") && !strings.Contains(code, "border-danger") {
		t.Fatalf("stripping comments left %d bytes with none of the markup in it", len(code))
	}
}

// stripSvelteComments removes // line comments and /* */ blocks so a check for
// a bad pattern cannot fire on prose describing that pattern.
func stripSvelteComments(src string) string {
	var out strings.Builder
	for {
		i := strings.Index(src, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(src[i:], "*/")
		if j < 0 {
			break
		}
		out.WriteString(src[:i])
		src = src[i+j+2:]
	}
	out.WriteString(src)

	var kept []string
	for _, l := range strings.Split(out.String(), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "//") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}
