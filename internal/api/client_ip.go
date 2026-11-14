package api

import (
	"net"
	"net/http"
	"strings"
)

// trustedHop reports whether an address is one a forwarding header may be
// believed from, or skipped over inside one: loopback, a private network, the
// tailnet, or Cloudflare's edge. It is the same list blocks.go refuses to block,
// for the same reason — these are the machines between a visitor and the panel.
func trustedHop(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range neverBlockCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// clientIP replaces chi's RealIP, which believed True-Client-IP, X-Real-IP and
// the FIRST X-Forwarded-For entry from anyone at all. Two things were wrong with
// what that left in r.RemoteAddr:
//
//   - Sent a header, it was whatever the caller typed — so the address in the
//     audit log, and the key of every per-IP limit, was the attacker's choice.
//   - Sent none, it was "ip:port", and the port is new on every connection. The
//     limiter counted five attempts per connection, which is no limit.
//
// Now a forwarding header is only read when the connection itself comes from a
// proxy we could plausibly be behind, and X-Forwarded-For is read from the right:
// each proxy appends the address it saw, so the last entry that is not itself a
// proxy is the nearest thing to a fact in that header. Everything to its left is
// whatever the client claimed.
//
// What this does not fix: somebody already on the LAN or the tailnet connects
// from a trusted address and can still claim to be anyone. They are inside the
// boundary this panel has always assumed.
func clientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = resolveClientIP(r.RemoteAddr, r.Header)
		next.ServeHTTP(w, r)
	})
}

func resolveClientIP(remoteAddr string, h http.Header) string {
	peer := hostOnly(remoteAddr)
	if !trustedHop(net.ParseIP(peer)) {
		return peer
	}
	var hops []string
	for _, line := range h.Values("X-Forwarded-For") {
		for _, e := range strings.Split(line, ",") {
			hops = append(hops, strings.TrimSpace(e))
		}
	}
	nearest := ""
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			break // not an address: nothing further left can be believed either
		}
		nearest = ip.String()
		if !trustedHop(ip) {
			return nearest
		}
	}
	if nearest != "" {
		return nearest // every hop was internal: a LAN client behind the proxy
	}
	if ip := net.ParseIP(strings.TrimSpace(h.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	return peer
}

// requestIsHTTPS reports whether the browser's side of this request was TLS.
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
