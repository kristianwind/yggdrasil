package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/auth"
)

// maxRequestBody caps JSON request bodies. decodeJSON is used by nearly every
// state-changing handler, including the pre-auth login endpoint, so an
// unbounded body would let an unauthenticated client exhaust memory. 1 MiB is
// far above any legitimate JSON payload here (file uploads use multipart, not
// decodeJSON, and have their own limits).
const maxRequestBody = 1 << 20

type contextKey string

const claimsKey contextKey = "claims"

func withClaims(ctx context.Context, c *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

func claimsFromContext(ctx context.Context) *auth.Claims {
	c, _ := ctx.Value(claimsKey).(*auth.Claims)
	return c
}

func extractToken(r *http.Request) string {
	// Authorization: Bearer <token>
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	// Cookie fallback (set on login; used by WebSocket handshakes too)
	if cookie, err := r.Cookie("ygg_token"); err == nil {
		return cookie.Value
	}
	// Query-param fallback (WebSockets can't set headers)
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(v)
}
