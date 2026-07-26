// Package backup creates and restores server archives and ships them to storage
// targets (local/NFS/CIFS mount, SFTP, SMB). Credentials are encrypted at rest
// by the caller; this package never logs them.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Archive writes a gzip-compressed tar of dataDir to w. When include is
// non-empty, only those top-level paths (files or directories) are archived —
// matching a gameskill's backup.include. Paths are stored relative to dataDir.
//
// A file that can't be read (permission denied, or it vanished mid-walk) is
// recorded in skipped and the archive continues — one unreadable file must not
// fail the whole backup. The caller should surface skipped so a partial archive
// isn't mistaken for a complete one. err is reserved for fatal, archive-level
// failures (a broken tar/gzip stream or a closed sink).
func Archive(dataDir string, include []string, w io.Writer) (skipped []string, err error) {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	roots := include
	if len(roots) == 0 {
		roots = []string{"."}
	}

	for _, root := range roots {
		base := filepath.Join(dataDir, root)
		info, err := os.Stat(base)
		if err != nil {
			// An included path that doesn't exist is skipped, not fatal.
			continue
		}
		if !info.IsDir() {
			sk, ferr := addFile(tw, dataDir, base)
			if ferr != nil {
				return skipped, ferr
			}
			if sk != "" {
				skipped = append(skipped, sk)
			}
			continue
		}
		werr := filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				// An entry we can't even stat (e.g. a dir we can't read into):
				// note it and keep walking the rest of the tree.
				if rel, rerr := filepath.Rel(dataDir, path); rerr == nil {
					skipped = append(skipped, filepath.ToSlash(rel))
				}
				return nil
			}
			if fi.IsDir() {
				return nil // tar entries for files carry their dir path
			}
			sk, ferr := addFile(tw, dataDir, path)
			if ferr != nil {
				return ferr // fatal: broken tar/gzip sink
			}
			if sk != "" {
				skipped = append(skipped, sk)
			}
			return nil
		})
		if werr != nil {
			return skipped, werr
		}
	}
	return skipped, nil
}

// addFile writes one path into the archive. It returns a non-empty rel path in
// skip when the file couldn't be read (permission denied / vanished) so the
// caller can record it and move on; err is returned only for a fatal write to
// the tar stream, which must abort the whole archive.
func addFile(tw *tar.Writer, dataDir, path string) (skip string, err error) {
	rel, err := filepath.Rel(dataDir, path)
	if err != nil {
		return "", err
	}
	relSlash := filepath.ToSlash(rel)
	fi, err := os.Lstat(path)
	if err != nil {
		return relSlash, nil // vanished mid-walk → skip, not fatal
	}
	// Skip sockets/devices; only regular files and symlinks are archived.
	if !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
		return "", nil
	}

	var link string
	if fi.Mode()&os.ModeSymlink != 0 {
		link, _ = os.Readlink(path)
	}

	// Open a regular file BEFORE writing its header, so an unreadable file is
	// skipped cleanly rather than leaving an orphan header (and a corrupt tar)
	// when the read fails after the header is already written.
	var f *os.File
	if fi.Mode().IsRegular() {
		f, err = os.Open(path)
		if err != nil {
			return relSlash, nil // permission denied etc → skip, not fatal
		}
		defer f.Close()
	}

	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return relSlash, nil // couldn't build a header → skip rather than abort
	}
	hdr.Name = relSlash
	if err := tw.WriteHeader(hdr); err != nil {
		return "", err // fatal: the archive stream is broken
	}
	if f != nil {
		if _, err := io.Copy(tw, f); err != nil {
			return "", err // fatal: write to the tar sink failed
		}
	}
	return "", nil
}

// Restore extracts a gzip-tar archive from r into destDir, guarding against
// path-traversal entries.
// Verify streams a backup archive and confirms it is a well-formed gzip+tar that
// decompresses in full — a cheap integrity check (a truncated or corrupt archive
// fails at the gzip CRC or a short tar entry) that reads everything but writes
// nothing to disk. Returns the file count and total uncompressed size.
func Verify(r io.Reader) (entries int, total int64, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("not a valid gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return entries, total, fmt.Errorf("corrupt tar stream: %w", err)
		}
		n, err := io.Copy(io.Discard, tr) // reading the body validates the gzip CRC as we go
		if err != nil {
			return entries, total, fmt.Errorf("truncated entry %q: %w", hdr.Name, err)
		}
		entries++
		total += n
	}
	return entries, total, nil
}

func Restore(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(absDest, filepath.Clean("/"+hdr.Name))
		if target != absDest && !strings.HasPrefix(target, absDest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Zip-slip via symlink: reject links pointing outside the destination,
			// otherwise a later entry could be written through them onto host files.
			resolved := hdr.Linkname
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), resolved)
			}
			resolved = filepath.Clean(resolved)
			if resolved != absDest && !strings.HasPrefix(resolved, absDest+string(os.PathSeparator)) {
				return fmt.Errorf("archive symlink escapes destination: %s -> %s", hdr.Name, hdr.Linkname)
			}
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			// O_NOFOLLOW: if target already exists as a symlink, fail instead of
			// writing through it (symlink-then-file ordering defense).
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // size bounded by archive
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}
