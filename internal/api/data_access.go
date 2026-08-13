package api

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Many container images start as root, chown their data directory to PUID:PGID
// and then drop to it. That directory is the panel's own, and PUID defaults to
// 1000 in most of them while the panel's service account is whatever
// `useradd --system` picked — 999 on two boxes here, 996 on a third, never
// 1000. So the container quietly takes the server's files, and afterwards the
// Files tab, restores and every panel-side write fail on that one server. The
// app itself keeps working, which is why nobody notices.
//
// Found three times before this existed: qBittorrentVPN, Tracefinity, and a
// Readarr with 23 files owned 1000:1000 that turned up only because someone
// went looking through the whole catalogue by hand.
//
// The check is a write, not a comparison of uids. Comparing would flag runes
// that use a different uid on purpose — mosquitto runs as the broker's own
// 1883 and documents why — while missing a directory that matches on paper and
// still cannot be written to because of its mode. Trying to write answers the
// only question that matters: can the panel still manage this server's files?
const (
	dataAccessScan   = time.Hour
	dataAccessRepeat = 24 * time.Hour
	dataAccessProbe  = ".yggdrasil-write-probe"
)

func (s *Server) startDataAccessLoop() {
	go func() {
		defer recoverLog("dataAccessLoop")
		// One pass shortly after boot, so a panel that has just been restarted
		// onto a broken server says so without waiting an hour.
		time.Sleep(2 * time.Minute)
		s.checkDataAccess()
		t := time.NewTicker(dataAccessScan)
		defer t.Stop()
		for range t.C {
			s.checkDataAccess()
		}
	}()
}

func (s *Server) checkDataAccess() {
	defer recoverLog("checkDataAccess")

	rows, err := s.db.Query(
		`SELECT id, name, COALESCE(data_dir,'') FROM servers WHERE installed = 1 AND COALESCE(data_dir,'') <> ''`)
	if err != nil {
		return
	}
	type srv struct{ id, name, dir string }
	var list []srv
	for rows.Next() {
		var x srv
		if rows.Scan(&x.id, &x.name, &x.dir) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		if _, err := os.Stat(x.dir); err != nil {
			continue // gone or not mounted yet — not this check's business
		}
		if writable(x.dir) {
			continue
		}
		owner := ownerOf(x.dir)
		title := x.name + ": the panel can no longer write to this server's files"
		detail := "Its data directory is not writable by the panel" + owner + ".\n" +
			"Backups, restores and the Files tab will fail for this server until that is fixed. " +
			"The usual cause is a container image that chowns the directory to its own PUID — " +
			"set PUID/PGID on the server to the panel's own account and recreate the container."
		if s.raiseIncident(x.id, "data-access", title, detail, dataAccessRepeat) {
			go s.notifyServer(x.id, "🔒 "+title+"\n"+detail)
		}
	}
}

// writable reports whether this process can create a file in dir. A probe is
// used rather than checking the mode bits because the answer depends on uid,
// gid, supplementary groups and the mode together, and getting that arithmetic
// subtly wrong is how a check like this ends up lying in the reassuring
// direction.
func writable(dir string) bool {
	p := filepath.Join(dir, dataAccessProbe)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(p) //nolint:errcheck // best effort; a leftover probe is harmless
	return true
}

// ownerOf describes a directory's owner for the message, so the admin is told
// which uid took it rather than being left to go and look. Best effort: the
// message is useful without it.
func ownerOf(dir string) string {
	fi, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf(" (owned by %d:%d, panel runs as %d:%d)",
			st.Uid, st.Gid, os.Getuid(), os.Getgid())
	}
	return ""
}
