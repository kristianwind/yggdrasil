// Package migrate moves a whole Yggdrasil panel between hosts as one portable
// bundle: the database, every server's data directory, and the auth secret_key
// (without which the encrypted variable secrets — RCON passwords, API keys —
// would be unrecoverable on the new host).
//
// The bundle is a gzip tar:
//
//	manifest.json      metadata + the secret_key + the server→data-dir map
//	db/<name>          a consistent snapshot of the SQLite database
//	data/<id>/…        each server's data directory, keyed by server id
//
// Import writes each data dir back to the path the manifest recorded, so the DB
// (which stores absolute data_dir paths) stays consistent — the two hosts must
// therefore use the same install layout (the default, so it just works).
//
// SECURITY: the bundle is a full-panel credential — it holds the secret_key, the
// user password hashes and every encrypted secret. Treat it like a private key.
package migrate

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/config"
)

const bundleVersion = 1

// Manifest is the bundle's index, read first on import.
type Manifest struct {
	Version    int         `json:"version"`
	ExportedAt string      `json:"exported_at"`
	SecretKey  string      `json:"secret_key"`
	DBFile     string      `json:"db_file"` // basename under db/
	Servers    []ServerRef `json:"servers"`
}

// ServerRef records where a server's data lived, so import restores it there.
type ServerRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	DataDir string `json:"data_dir"`
}

// now is overridable in tests (the package must not read the wall clock in a way
// that makes bundles non-reproducible for assertions).
var now = time.Now

// Export writes a migration bundle of the panel to w. It snapshots the database
// with VACUUM INTO for a consistent copy even if the panel is running, though
// stopping it first is still recommended so no data dir is mid-write.
func Export(cfg *config.Config, db *sql.DB, w io.Writer) error {
	rows, err := db.Query("SELECT id, name, data_dir FROM servers")
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	var servers []ServerRef
	for rows.Next() {
		var s ServerRef
		if err := rows.Scan(&s.ID, &s.Name, &s.DataDir); err != nil {
			rows.Close()
			return err
		}
		servers = append(servers, s)
	}
	rows.Close()

	man := Manifest{
		Version:    bundleVersion,
		ExportedAt: now().UTC().Format(time.RFC3339),
		SecretKey:  cfg.Auth.SecretKey,
		DBFile:     filepath.Base(cfg.Database.Path),
		Servers:    servers,
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	manJSON, _ := json.MarshalIndent(man, "", "  ")
	if err := writeEntry(tw, "manifest.json", manJSON); err != nil {
		return err
	}

	// A consistent snapshot of the live SQLite DB.
	snap, err := os.CreateTemp("", "ygg-migrate-*.db")
	if err != nil {
		return err
	}
	snapPath := snap.Name()
	snap.Close()
	defer os.Remove(snapPath)
	if _, err := db.Exec("VACUUM INTO ?", snapPath); err != nil {
		return fmt.Errorf("snapshot db: %w", err)
	}
	snapBytes, err := os.ReadFile(snapPath)
	if err != nil {
		return err
	}
	if err := writeEntry(tw, "db/"+man.DBFile, snapBytes); err != nil {
		return err
	}

	// Each server's whole data directory. A missing dir (a server not yet
	// installed, or one whose data was removed) is skipped, not fatal — the DB row
	// still migrates, so its config comes across; there's simply no data to carry.
	for _, s := range servers {
		if s.DataDir == "" {
			continue
		}
		if fi, err := os.Stat(s.DataDir); err != nil || !fi.IsDir() {
			fmt.Fprintf(os.Stderr, "migrate: skipping %s — data dir %q not present\n", s.Name, s.DataDir)
			continue
		}
		if err := addTree(tw, s.DataDir, "data/"+s.ID); err != nil {
			return fmt.Errorf("archive %s: %w", s.Name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Import reads a bundle from r, writes the database to dbPath and every server's
// data directory back to its recorded path, and returns the manifest so the
// caller can install the secret_key into the new host's config. Guards every
// entry against path traversal.
func Import(r io.Reader, dbPath string) (*Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)

	var man *Manifest
	dataRoots := map[string]string{} // "data/<id>" prefix → absolute dest dir

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return nil, fmt.Errorf("unsafe entry %q", hdr.Name)
		}

		switch {
		case name == "manifest.json":
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			var m Manifest
			if err := json.Unmarshal(b, &m); err != nil {
				return nil, fmt.Errorf("manifest: %w", err)
			}
			if m.Version != bundleVersion {
				return nil, fmt.Errorf("bundle version %d, this build imports %d", m.Version, bundleVersion)
			}
			man = &m
			for _, s := range m.Servers {
				dataRoots["data/"+s.ID] = s.DataDir
			}

		case strings.HasPrefix(name, "db/"):
			if err := writeFile(dbPath, tr, hdr.FileInfo().Mode()); err != nil {
				return nil, err
			}

		case strings.HasPrefix(name, "data/"):
			if man == nil {
				return nil, fmt.Errorf("manifest must come before data (corrupt bundle)")
			}
			dest, ok := destFor(name, dataRoots)
			if !ok {
				return nil, fmt.Errorf("data entry %q has no server in the manifest", hdr.Name)
			}
			if hdr.Typeflag == tar.TypeDir {
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return nil, err
				}
				continue
			}
			if err := writeFile(dest, tr, hdr.FileInfo().Mode()); err != nil {
				return nil, err
			}
		}
	}
	if man == nil {
		return nil, fmt.Errorf("no manifest in bundle")
	}
	return man, nil
}

// destFor maps a "data/<id>/rel/path" entry to <dataDir>/rel/path using the
// manifest's server→dir map. Returns false for an entry under no known server.
func destFor(name string, roots map[string]string) (string, bool) {
	for prefix, dir := range roots {
		if name == prefix {
			return dir, true
		}
		if rel := strings.TrimPrefix(name, prefix+"/"); rel != name {
			return filepath.Join(dir, filepath.Clean("/"+rel)), true
		}
	}
	return "", false
}

func writeEntry(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// addTree tars every file under root into the bundle under prefix.
func addTree(tw *tar.Writer, root, prefix string) error {
	return filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := prefix
		if rel != "." {
			name = prefix + "/" + filepath.ToSlash(rel)
		}
		link := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
}

func writeFile(dest string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}
