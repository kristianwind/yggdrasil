package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every route the panel serves must be gated, and this test is the thing that
// says so. Two earlier audit passes found the same bug twice — a route that
// reaches a specific server while checking only that *somebody* is logged in
// (the install-log WebSocket streamed another server's build output; realm
// CRUD let a non-admin delete a permission scope). Both were one missing line
// in a 290-route file, and both were found by a human reading the router.
//
// So assert the property instead: a route is acceptable only if it is
// deliberately public (named below), admin-gated at the router, or reaches a
// permission check — in its own handler or in a helper it calls. Anything else
// fails, and the fix is either the missing check or a line in publicRoutes
// with a reason.
//
// The gate must be REACHED, not merely mentioned: file handling looked
// unguarded to a first version of this test because its check lives one level
// down in serverDataDir. Following calls is what makes the answer decidable by
// looking, rather than a guess that happens to be right.

// publicRoutes are served without a session, on purpose. Adding to this list is
// a security decision: write why.
var publicRoutes = map[string]string{
	"POST /api/auth/login":                                "the login form itself",
	"POST /api/auth/forgot":                               "password reset request; no-enumeration by design",
	"POST /api/auth/reset":                                "consumes an emailed single-use token",
	"POST /api/auth/passkey/login/begin":                  "WebAuthn challenge, pre-session",
	"POST /api/auth/passkey/login/finish":                 "WebAuthn assertion, pre-session",
	"GET /api/version":                                    "version string; the updater and the docs site read it",
	"GET /api/status":                                     "opt-in public status page data; 404s when off",
	"GET /status":                                         "opt-in public status page; 404s when off",
	"GET /status.js":                                      "script for the public status page",
	"POST /api/beacon":                                    "beacon receiver; 404s unless this instance collects",
	"GET /api/beacon/count":                               "public install count; 404s unless a collector opted in",
	"GET /robots.txt":                                     "crawler directives, public by definition",
	"GET /.well-known/oauth-protected-resource":           "RFC 9728 discovery, public by spec",
	"GET /.well-known/oauth-protected-resource/api/mcp":   "RFC 9728 discovery, public by spec",
	"GET /.well-known/oauth-authorization-server":         "RFC 8414 discovery, public by spec",
	"GET /.well-known/oauth-authorization-server/api/mcp": "RFC 8414 discovery, public by spec",
	"POST /oauth/register":                                "RFC 7591 dynamic client registration",
	"GET /oauth/authorize":                                "authorization endpoint; authenticates the user itself",
	"POST /oauth/authorize":                               "consent submit; authenticates the user itself",
	"POST /oauth/token":                                   "token endpoint; authenticates by client credentials + PKCE",
}

// ownAccountRoutes act on the caller's own account or on data the session
// already implies. They need a session and nothing more.
var ownAccountRoutes = map[string]bool{
	"POST /api/auth/logout": true, "GET /api/auth/me": true,
	"GET /api/auth/2fa": true, "POST /api/auth/2fa/setup": true,
	"POST /api/auth/2fa/enable": true, "POST /api/auth/2fa/disable": true,
	"GET /api/auth/passkey/credentials":         true,
	"POST /api/auth/passkey/register/begin":     true,
	"POST /api/auth/passkey/register/finish":    true,
	"PUT /api/auth/passkey/credentials/{id}":    true,
	"DELETE /api/auth/passkey/credentials/{id}": true,
	"GET /api/tokens":                           true, "POST /api/tokens": true, "DELETE /api/tokens/{id}": true,
	"GET /api/mcp/info": true, "GET /api/mcp/connections": true,
	"DELETE /api/mcp/connections/{id}": true,
	"POST /api/mcp":                    true, "GET /api/mcp": true, "DELETE /api/mcp": true,
	"GET /api/gameskills/{id}": true, "GET /api/templates": true,
	"GET /api/mods/icon":                true,
	"PUT /api/realms/{id}/collapsed":    true,
	"GET /api/settings/network":         true,
	"GET /api/servers/{id}/import-data": true,
	"GET /api/servers/{id}/app-update":  true,
}

type route struct {
	key     string // "GET /api/x"
	handler string
	authed  bool
	admin   bool
	line    int
}

func collectRoutes(t *testing.T) []route {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	var out []route
	// walk descends a statement list, carrying whether an enclosing r.Group has
	// already installed the auth middleware.
	var walk func(n ast.Node, authed bool)
	walk = func(n ast.Node, authed bool) {
		body, ok := n.(*ast.BlockStmt)
		if !ok {
			return
		}
		// A r.Use(s.authMiddleware) anywhere in THIS block covers the whole block.
		for _, st := range body.List {
			if call := callOf(st); call != nil && selName(call.Fun) == "Use" {
				if strings.Contains(exprText(fset, call), "authMiddleware") {
					authed = true
				}
			}
		}
		for _, st := range body.List {
			call := callOf(st)
			if call == nil {
				continue
			}
			name := selName(call.Fun)
			switch name {
			case "Get", "Post", "Put", "Delete", "Patch":
				if len(call.Args) < 2 {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				p, _ := strconv.Unquote(lit.Value)
				h := exprText(fset, call.Args[1])
				out = append(out, route{
					key:     strings.ToUpper(name) + " " + p,
					handler: lastHandlerName(h),
					authed:  authed,
					admin:   strings.Contains(h, "requireAdmin"),
					line:    fset.Position(call.Pos()).Line,
				})
			case "Group", "Route":
				for _, a := range call.Args {
					if fl, ok := a.(*ast.FuncLit); ok {
						walk(fl.Body, authed)
					}
				}
			}
		}
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			walk(fd.Body, false)
		}
	}
	return out
}

func callOf(st ast.Stmt) *ast.CallExpr {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	c, _ := es.X.(*ast.CallExpr)
	return c
}

func selName(e ast.Expr) string {
	if se, ok := e.(*ast.SelectorExpr); ok {
		return se.Sel.Name
	}
	return ""
}

func exprText(fset *token.FileSet, n ast.Node) string {
	start := fset.Position(n.Pos())
	end := fset.Position(n.End())
	src, err := os.ReadFile("server.go")
	if err != nil {
		return ""
	}
	if start.Offset < 0 || end.Offset > len(src) || start.Offset >= end.Offset {
		return ""
	}
	return string(src[start.Offset:end.Offset])
}

var handlerRe = regexp.MustCompile(`s\.(handle\w+)`)

func lastHandlerName(expr string) string {
	m := handlerRe.FindAllStringSubmatch(expr, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// methodBodies maps every method on *Server to its source text.
func methodBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	decl := regexp.MustCompile(`func \(s \*Server\) (\w+)\(`)
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		src := string(b)
		locs := decl.FindAllStringSubmatchIndex(src, -1)
		for i, loc := range locs {
			name := src[loc[2]:loc[3]]
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			out[name] = src[loc[1]:end]
		}
	}
	return out
}

var callRe = regexp.MustCompile(`s\.(\w+)\(`)

// reachesCheck reports whether name, or anything it calls within maxDepth,
// performs a permission check. Depth 2 is what the real code needs
// (handler → serverDataDir → s.can); the limit keeps a cycle from hanging.
func reachesCheck(name string, bodies map[string]string, seen map[string]bool, depth int) bool {
	if depth > 2 || name == "" || seen[name] {
		return false
	}
	seen[name] = true
	b, ok := bodies[name]
	if !ok {
		return false
	}
	if strings.Contains(b, "s.can(") || strings.Contains(b, "isAdmin(r)") {
		return true
	}
	for _, m := range callRe.FindAllStringSubmatch(b, -1) {
		if reachesCheck(m[1], bodies, seen, depth+1) {
			return true
		}
	}
	return false
}

func TestEveryRouteIsGated(t *testing.T) {
	routes := collectRoutes(t)
	// A sweep that parses nothing passes over nothing. The panel has hundreds of
	// routes; anything near zero means the parser broke, not that the router did.
	if len(routes) < 200 {
		t.Fatalf("parsed only %d routes from server.go — the parser is broken, "+
			"and every assertion below would be vacuous", len(routes))
	}
	bodies := methodBodies(t)
	if len(bodies) < 100 {
		t.Fatalf("found only %d *Server methods — the body scan is broken", len(bodies))
	}

	var ungated, undeclaredPublic []string
	for _, r := range routes {
		if !r.authed {
			if _, ok := publicRoutes[r.key]; !ok {
				undeclaredPublic = append(undeclaredPublic,
					r.key+"  (server.go:"+strconv.Itoa(r.line)+", "+r.handler+")")
			}
			continue
		}
		if r.admin || ownAccountRoutes[r.key] {
			continue
		}
		if !reachesCheck(r.handler, bodies, map[string]bool{}, 0) {
			ungated = append(ungated,
				r.key+"  (server.go:"+strconv.Itoa(r.line)+", "+r.handler+")")
		}
	}
	sort.Strings(ungated)
	sort.Strings(undeclaredPublic)

	if len(undeclaredPublic) > 0 {
		t.Errorf("%d route(s) are served WITHOUT a session and are not declared in publicRoutes:\n  %s\n\n"+
			"Either move the route inside the authenticated group, or add it to publicRoutes with the reason it is safe.",
			len(undeclaredPublic), strings.Join(undeclaredPublic, "\n  "))
	}
	if len(ungated) > 0 {
		t.Errorf("%d authenticated route(s) reach no permission check:\n  %s\n\n"+
			"A session alone is not authorisation — any signed-in user reaches these. Add s.can(...) "+
			"for the right rbac permission (or requireAdmin at the router). If the route genuinely "+
			"acts only on the caller's own account, add it to ownAccountRoutes.",
			len(ungated), strings.Join(ungated, "\n  "))
	}
	t.Logf("checked %d routes: %d public, %d gated", len(routes), len(publicRoutes), len(routes)-len(publicRoutes))
}
