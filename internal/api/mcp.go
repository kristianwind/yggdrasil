package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Model Context Protocol endpoint — one HTTP address that lets Claude (or any
// other MCP client) read and drive this panel: "is anything down?", "show me the
// last hundred lines from the Minecraft server", "restart it".
//
// Transport is Streamable HTTP (MCP 2025-06-18): a single endpoint, JSON-RPC 2.0
// in the POST body, one JSON object back. No SSE stream and no session id — this
// server never speaks first, so there is nothing to stream and no state to keep
// between calls, and the spec makes both optional. GET and DELETE answer 405,
// which is the documented way to say so.
//
// Authentication is the panel's existing API token: `Authorization: Bearer
// ygg_…`, handled by authMiddleware before anything here runs, so an MCP client
// acts as the token's owner and gets exactly that user's permissions. The same
// middleware rejects a cross-origin POST, which covers the DNS-rebinding attack
// the MCP transport spec warns about.
//
// Every tool that DOES something runs the panel's own HTTP handler in-process
// rather than reimplementing it (see callAPI). A tool and a button are then the
// same code path: the same permission checks, the same audit entry, the same
// notifications, and no second implementation to drift.

const (
	// The version this server implements. Older clients are answered in their own
	// version when we can speak it, per the lifecycle spec's negotiation rule.
	mcpProtocolVersion = "2025-06-18"
	mcpMaxLogLines     = 500
	mcpDefaultLogLines = 100
)

var mcpSupportedVersions = map[string]bool{
	"2025-06-18": true,
	"2025-03-26": true,
	"2024-11-05": true,
}

// jsonrpcRequest is one incoming message. A message with no `id` is a
// notification: it gets 202 and no reply, never an error response.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// handleMCPUnsupported answers the methods this endpoint deliberately does not
// implement. 405 on GET is the spec's way of saying "no server-initiated
// stream"; 405 on DELETE says sessions are not in use.
func (s *Server) handleMCPUnsupported(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	jsonError(w, "this MCP endpoint has no server-initiated stream; POST JSON-RPC requests instead", http.StatusMethodNotAllowed)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// An unsupported protocol version must be refused at the HTTP layer, before
	// the body is read (transport spec). An absent header means either the first
	// request of a session or a 2025-03-26 client, both of which are fine.
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !mcpSupportedVersions[v] {
		jsonError(w, "unsupported MCP-Protocol-Version: "+v, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, "read body", http.StatusBadRequest)
		return
	}
	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		mcpWrite(w, jsonrpcResponse{JSONRPC: "2.0", Error: &jsonrpcError{Code: -32700, Message: "parse error"}})
		return
	}

	// No id = notification or response. The client is telling us something
	// (notifications/initialized, notifications/cancelled); acknowledge and stop.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := s.mcpDispatch(r, req)
	if rpcErr != nil {
		mcpWrite(w, jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
		return
	}
	mcpWrite(w, jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func mcpWrite(w http.ResponseWriter, resp jsonrpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (s *Server) mcpDispatch(r *http.Request, req jsonrpcRequest) (any, *jsonrpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p) //nolint:errcheck
		// Answer in the client's version when we speak it, otherwise in ours and
		// let the client decide whether to continue.
		version := mcpProtocolVersion
		if mcpSupportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "yggdrasil", "title": s.panelName(), "version": s.version},
			"instructions": "Yggdrasil manages game and app servers as Docker containers. " +
				"Tools take a server's NAME as shown in the panel (an id also works). " +
				"Stopping or restarting disconnects anyone connected, so confirm with the user first.",
		}, nil

	case "ping":
		// Required by the spec's utilities; an empty result is the whole contract.
		return map[string]any{}, nil

	case "tools/list":
		list := make([]map[string]any, 0, len(mcpTools))
		for _, t := range mcpTools {
			list = append(list, map[string]any{
				"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.InputSchema,
			})
		}
		return map[string]any{"tools": list}, nil

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		for _, t := range mcpTools {
			if t.Name != p.Name {
				continue
			}
			// A tool that fails reports it in the RESULT with isError, not as a
			// JSON-RPC error: the model is meant to see the reason and decide what
			// to do, which a protocol-level error hides from it.
			text, err := t.Run(s, r, p.Arguments)
			if err != nil {
				return map[string]any{
					"content": []map[string]any{{"type": "text", "text": err.Error()}},
					"isError": true,
				}, nil
			}
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": false,
			}, nil
		}
		return nil, &jsonrpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	return nil, &jsonrpcError{Code: -32601, Message: "method not found: " + req.Method}
}

// mcpTool is one callable. Run returns the text the model sees; an error becomes
// an isError result carrying that message.
type mcpTool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Run         func(s *Server, r *http.Request, args map[string]any) (string, error)
}

// serverArg is the one argument nearly every tool takes.
var serverArgSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"server": map[string]any{"type": "string", "description": "Server name as shown in the panel, or its id"},
	},
	"required": []string{"server"},
}

var mcpTools = []mcpTool{
	{
		Name:        "list_servers",
		Title:       "List servers",
		Description: "Every server this token's owner may see, with its status, rune, ports and realm. Start here — the other tools take a name from this list.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(s *Server, r *http.Request, _ map[string]any) (string, error) {
			return s.mcpJSON(r, s.handleListServers, "GET", "/api/servers", nil, nil)
		},
	},
	{
		Name:        "get_server",
		Title:       "Server detail",
		Description: "One server in full: status, ports, resource limits, install state, rune and version.",
		InputSchema: serverArgSchema,
		Run: func(s *Server, r *http.Request, args map[string]any) (string, error) {
			id, err := s.mcpResolveServer(r, args)
			if err != nil {
				return "", err
			}
			return s.mcpJSON(r, s.handleGetServer, "GET", "/api/servers/"+id, map[string]string{"id": id}, nil)
		},
	},
	{
		Name:  "server_logs",
		Title: "Read server logs",
		Description: "The tail of a server's container log — the first thing to read when something is wrong or a server will not start. " +
			"Returns plain text, newest last.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "description": "Server name as shown in the panel, or its id"},
				"lines":  map[string]any{"type": "integer", "description": "How many lines from the end (default 100, max 500)"},
			},
			"required": []string{"server"},
		},
		Run: func(s *Server, r *http.Request, args map[string]any) (string, error) {
			id, err := s.mcpResolveServer(r, args)
			if err != nil {
				return "", err
			}
			lines := mcpDefaultLogLines
			if n, ok := args["lines"].(float64); ok && n > 0 {
				lines = int(n)
			}
			if lines > mcpMaxLogLines {
				lines = mcpMaxLogLines
			}
			path := fmt.Sprintf("/api/servers/%s/logs/export?tail=%d", id, lines)
			out, err := s.mcpJSON(r, s.handleExportServerLogs, "GET", path, map[string]string{"id": id}, nil)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(out) == "" {
				return "(no log output — the server may never have started)", nil
			}
			return out, nil
		},
	},
	{
		Name:        "start_server",
		Title:       "Start a server",
		Description: "Start a stopped server. Safe: the worst case is a server that is already running.",
		InputSchema: serverArgSchema,
		Run: func(s *Server, r *http.Request, args map[string]any) (string, error) {
			return s.mcpControl(r, args, s.handleStartServer, "start", "starting")
		},
	},
	{
		Name:  "stop_server",
		Title: "Stop a server",
		Description: "Stop a running server. Anyone connected is disconnected immediately — confirm with the user before calling this. " +
			"The rune's own save and shutdown commands run first, so the world is flushed to disk.",
		InputSchema: serverArgSchema,
		Run: func(s *Server, r *http.Request, args map[string]any) (string, error) {
			return s.mcpControl(r, args, s.handleStopServer, "stop", "stopped")
		},
	},
	{
		Name:  "restart_server",
		Title: "Restart a server",
		Description: "Restart a server now, with no warning to anyone connected — confirm with the user first. " +
			"This recreates the container, so rune, environment and mod changes take effect.",
		InputSchema: serverArgSchema,
		Run: func(s *Server, r *http.Request, args map[string]any) (string, error) {
			return s.mcpControl(r, args, s.handleRestartServer, "restart", "restarting")
		},
	},
	{
		Name:        "panel_status",
		Title:       "Panel status",
		Description: "One-line health of the panel itself: its version, and how many servers are running, stopped or in trouble. Answers \"is anything down?\" in a single call.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(s *Server, r *http.Request, _ map[string]any) (string, error) {
			raw, err := s.mcpJSON(r, s.handleListServers, "GET", "/api/servers", nil, nil)
			if err != nil {
				return "", err
			}
			var servers []struct {
				Name          string `json:"name"`
				Status        string `json:"status"`
				Installed     bool   `json:"installed"`
				InstallStatus string `json:"install_status"`
			}
			if err := json.Unmarshal([]byte(raw), &servers); err != nil {
				return "", fmt.Errorf("could not read the server list: %w", err)
			}
			counts := map[string]int{}
			trouble := []string{}
			for _, srv := range servers {
				counts[srv.Status]++
				if srv.InstallStatus == "error" {
					trouble = append(trouble, srv.Name+" (install failed)")
				}
			}
			sort.Strings(trouble)
			var b strings.Builder
			fmt.Fprintf(&b, "Yggdrasil %s — %d server(s)\n", s.version, len(servers))
			for _, st := range []string{"running", "starting", "stopped"} {
				if counts[st] > 0 {
					fmt.Fprintf(&b, "  %s: %d\n", st, counts[st])
				}
			}
			for st, n := range counts {
				if st != "running" && st != "starting" && st != "stopped" {
					fmt.Fprintf(&b, "  %s: %d\n", st, n)
				}
			}
			if len(trouble) > 0 {
				fmt.Fprintf(&b, "needs attention: %s\n", strings.Join(trouble, ", "))
			}
			return b.String(), nil
		},
	},
}

// mcpControl runs one of the start/stop/restart handlers and reports the outcome
// in a sentence rather than as the handler's JSON, which says nothing a model can
// use.
func (s *Server) mcpControl(r *http.Request, args map[string]any, h http.HandlerFunc, verb, done string) (string, error) {
	id, err := s.mcpResolveServer(r, args)
	if err != nil {
		return "", err
	}
	name := s.serverName(id)
	if _, err := s.mcpJSON(r, h, "POST", fmt.Sprintf("/api/servers/%s/%s", id, verb), map[string]string{"id": id}, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is %s.", name, done), nil
}

// mcpResolveServer turns the `server` argument into an id. A model has the name
// the user says out loud, not a uuid, so names are matched first — exactly, then
// case-insensitively, and an ambiguous name is an error rather than a guess at
// which of two servers to restart.
func (s *Server) mcpResolveServer(r *http.Request, args map[string]any) (string, error) {
	q, _ := args["server"].(string)
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("which server? Pass its name as shown in the panel (list_servers shows them)")
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id, name FROM servers")
	if err != nil {
		return "", fmt.Errorf("could not read the server list")
	}
	defer rows.Close()
	var exact, fuzzy []string
	names := []string{}
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) != nil {
			continue
		}
		names = append(names, name)
		switch {
		case id == q || name == q:
			exact = append(exact, id)
		case strings.EqualFold(name, q) || strings.HasPrefix(id, q):
			fuzzy = append(fuzzy, id)
		}
	}
	match := exact
	if len(match) == 0 {
		match = fuzzy
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		sort.Strings(names)
		return "", fmt.Errorf("no server called %q. This panel has: %s", q, strings.Join(names, ", "))
	default:
		return "", fmt.Errorf("%q matches more than one server — use the id instead", q)
	}
}

// mcpJSON runs one of the panel's own handlers in-process, as the MCP caller,
// and returns its body. This is the whole reason a tool cannot drift from the
// button beside it: permission checks, audit entries, notifications and every
// side effect are the handler's, not a copy.
//
// The synthesized request carries the caller's claims and the chi URL params the
// handler reads, and is marked as coming from MCP so the audit trail can tell an
// AI-driven stop from a click.
func (s *Server) mcpJSON(r *http.Request, h http.HandlerFunc, method, path string, params map[string]string, body any) (string, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		payload = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yggdrasil-mcp")
	ctx := r.Context()
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))

	out := rec.Body.String()
	if rec.Code >= 400 {
		// Hand the handler's own reason to the model — "server is not installed
		// yet; run install first" is actionable, "HTTP 409" is not.
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(out), &e) == nil && e.Error != "" {
			return "", fmt.Errorf("%s", e.Error)
		}
		return "", fmt.Errorf("request failed (%d)", rec.Code)
	}
	return out, nil
}
