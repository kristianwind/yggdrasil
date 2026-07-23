package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DayZ's Linux server won't answer BattlEye RCon and returns blank names over A2S,
// so "who is online" and "ban" can't come from the query protocol. They can come
// from the admin log (.ADM, enabled by the rune's -adminlog): every join/leave is
// logged with the player's name and BIS id. Replaying those lines gives a live
// roster with real names; banning appends the id to the server's ban.txt (DayZ
// reads it on connect — so a ban takes effect on their next join; kicking a player
// already in-game still needs RCon, which DayZ-Linux lacks).

var (
	dzAdmConnectRe    = regexp.MustCompile(`Player\s+"([^"]*)"\s*\(id=([^\s)]+)[^)]*\)\s+is connected`)
	dzAdmDisconnectRe = regexp.MustCompile(`Player\s+"([^"]*)"\s*\(id=([^\s)]+)[^)]*\)\s+has been disconnected`)
	dzAdmTimeRe       = regexp.MustCompile(`^(\d{2}:\d{2}:\d{2})`)
)

type dayzRosterEntry struct {
	Name  string `json:"name"`
	ID    string `json:"id"`    // BIS id from the .ADM (used for ban.txt)
	Since string `json:"since"` // connect time HH:MM:SS from the log
}

// dayzNewestADM returns the current session's admin log (newest *.ADM under the
// server's DayZ home), or "" if none.
func dayzNewestADM(dataDir string) string {
	dir := filepath.Join(dataDir, ".local", "share", "DayZ")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".adm") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if m := info.ModTime().UnixNano(); m > newestMod {
			newestMod, newest = m, filepath.Join(dir, e.Name())
		}
	}
	return newest
}

// dayzADMRoster replays the current admin log's join/leave lines into the set of
// players currently connected — a player is online when their most recent event is
// a connect. Best-effort: an unreadable/absent log yields an empty roster.
func dayzADMRoster(dataDir string) []dayzRosterEntry {
	path := dayzNewestADM(dataDir)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Cap the replay window so a very long session's log stays cheap; recent events
	// (what determines who is on now) are at the end.
	const maxScan = 4 << 20
	if len(data) > maxScan {
		data = data[len(data)-maxScan:]
	}
	return parseDayzRoster(string(data))
}

// parseDayzRoster replays admin-log join/leave lines into the currently-connected
// set (a player is online when their last event is a connect). Pure + testable.
func parseDayzRoster(content string) []dayzRosterEntry {
	type live struct{ name, since string }
	online := map[string]live{}
	for _, line := range strings.Split(content, "\n") {
		ts := ""
		if m := dzAdmTimeRe.FindStringSubmatch(line); m != nil {
			ts = m[1]
		}
		if m := dzAdmConnectRe.FindStringSubmatch(line); m != nil {
			online[m[2]] = live{name: m[1], since: ts}
			continue
		}
		if m := dzAdmDisconnectRe.FindStringSubmatch(line); m != nil {
			delete(online, m[2])
		}
	}
	out := make([]dayzRosterEntry, 0, len(online))
	for id, l := range online {
		out = append(out, dayzRosterEntry{Name: l.name, ID: id, Since: l.since})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// dayzBanID appends a BIS id to the server's ban.txt (deduped), which DayZ reads to
// refuse the player on their next connect. Returns whether it was newly added.
func dayzBanID(dataDir, id string) (bool, error) {
	path := filepath.Join(dataDir, "ban.txt")
	existing, _ := os.ReadFile(path) // ok if absent
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == id {
			return false, nil // already banned
		}
	}
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += id + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
