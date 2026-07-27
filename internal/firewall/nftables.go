// Package firewall blocks individual client IPs at the host level via nftables,
// for origin traffic that does NOT arrive through a CDN (e.g. a site that can't be
// put behind Cloudflare). It owns exactly one dedicated table — `inet yggdrasil` —
// containing two named sets (v4/v6) and a single input chain that DROPs matching
// source IPs, and ONLY on the web ports (80/443). It never touches SSH or any other
// chain, so a bad entry can at worst drop web traffic from that IP — never lock the
// operator out of the box. Flushing the sets fully reverses everything.
//
// Managing nftables needs CAP_NET_ADMIN, which the panel does not hold by default,
// so every operation may fail with a permission error; callers surface that as an
// actionable message rather than treating the block as applied. The Cloudflare
// backend needs no host privileges and is preferred whenever the site is proxied.
package firewall

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const (
	tableName = "yggdrasil"
	set4      = "blocklist4"
	set6      = "blocklist6"
)

// ruleset is applied once (idempotently) to create the table, sets and drop chain.
// The chain hooks input at a low priority and only drops the web ports, so it can
// never affect SSH/administration. `flags interval` lets the sets hold /CIDR too,
// though we only add single hosts today.
const ruleset = `table inet yggdrasil {
	set blocklist4 {
		type ipv4_addr
		flags interval
	}
	set blocklist6 {
		type ipv6_addr
		flags interval
	}
	chain block_input {
		type filter hook input priority -10; policy accept;
		tcp dport { 80, 443 } ip saddr @blocklist4 drop
		tcp dport { 80, 443 } ip6 saddr @blocklist6 drop
	}
}
`

// nftPath is the resolved nft binary, or "" if unavailable.
func nftPath() string {
	if p, err := exec.LookPath("nft"); err == nil {
		return p
	}
	// Common absolute location when PATH is minimal under systemd.
	if _, err := exec.LookPath("/usr/sbin/nft"); err == nil {
		return "/usr/sbin/nft"
	}
	return ""
}

// Available reports whether nft exists AND the process has the privilege to manage
// the ruleset. It's a read-only probe (`nft list tables`) with NO side effects — it
// never creates the table — so it's safe to call from a settings GET. The table is
// created lazily on the first actual Block().
func Available() bool {
	if nftPath() == "" {
		return false
	}
	_, err := run("list", "tables")
	return err == nil
}

func run(args ...string) (string, error) {
	nft := nftPath()
	if nft == "" {
		return "", fmt.Errorf("nft binary not found (install nftables)")
	}
	cmd := exec.Command(nft, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "permission denied") || strings.Contains(strings.ToLower(msg), "operation not permitted") {
			return "", fmt.Errorf("nftables needs CAP_NET_ADMIN — grant it to the yggdrasil service (AmbientCapabilities=CAP_NET_ADMIN) or disable host-firewall blocking")
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("nft %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

var ensured bool

// Ensure creates the table/sets/chain if absent. It does NOT re-apply when the
// table already exists — re-applying the full ruleset errors ("set exists"), and a
// reset would wipe live blocks. The kernel keeps the table across a panel-process
// restart, so existing blocks survive without re-adding them (only a host reboot
// clears the ruleset, since it isn't persisted to disk). Idempotent; safe to call
// often (Block/Unblock call it first).
func Ensure() error {
	if ensured {
		return nil
	}
	nft := nftPath()
	if nft == "" {
		return fmt.Errorf("nft binary not found (install nftables)")
	}
	// Already present (created by an earlier call/process)? Trust it and don't touch
	// its contents.
	if _, err := run("list", "table", "inet", tableName); err == nil {
		ensured = true
		return nil
	}
	cmd := exec.Command(nft, "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "permission denied") || strings.Contains(strings.ToLower(msg), "operation not permitted") {
			return fmt.Errorf("nftables needs CAP_NET_ADMIN — grant it to the yggdrasil service or disable host-firewall blocking")
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("nft init: %s", msg)
	}
	ensured = true
	return nil
}

// setFor returns the set name for an address family.
func setFor(ip net.IP) string {
	if ip.To4() == nil {
		return set6
	}
	return set4
}

// Block adds ip to the appropriate set (drops its web traffic). Idempotent.
func Block(ip string) error {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return fmt.Errorf("invalid IP %q", ip)
	}
	if err := Ensure(); err != nil {
		return err
	}
	// `add element` is idempotent for an existing member.
	_, err := run("add", "element", "inet", tableName, setFor(parsed), "{ "+parsed.String()+" }")
	return err
}

// Unblock removes ip from its set (no-op if absent).
func Unblock(ip string) error {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return fmt.Errorf("invalid IP %q", ip)
	}
	if err := Ensure(); err != nil {
		return err
	}
	_, err := run("delete", "element", "inet", tableName, setFor(parsed), "{ "+parsed.String()+" }")
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such") {
		return nil // already gone
	}
	return err
}
