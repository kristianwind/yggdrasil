// Package wpress reads All-in-One WP Migration archives (.wpress) — the format
// most WordPress admins actually hold their site in. It is a plain sequence of
// [header][content] records with no compression and no index:
//
//	name   255 bytes, NUL-padded — file name
//	size    14 bytes, ASCII decimal, NUL-padded — content length
//	mtime   12 bytes, ASCII decimal, NUL-padded
//	prefix 4096 bytes, NUL-padded — directory path relative to wp-content
//
// A block of all-zero bytes marks EOF. Paths are wp-content-relative; the two
// special top-level entries are package.json (site metadata) and database.sql
// (the dump, with the table prefix masked as SERVMASK_PREFIX).
package wpress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	nameLen   = 255
	sizeLen   = 14
	mtimeLen  = 12
	prefixLen = 4096
	headerLen = nameLen + sizeLen + mtimeLen + prefixLen
)

// Entry describes one record; Body must be fully consumed (or skipped via the
// reader) before calling Next again.
type Entry struct {
	Path string // cleaned, wp-content-relative ("plugins/x.php", "database.sql")
	Size int64
	Body io.Reader // exactly Size bytes
}

// Reader walks a .wpress stream sequentially.
type Reader struct {
	r    io.Reader
	body *io.LimitedReader // current entry's body
}

func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Next returns the next entry, draining any unread remainder of the previous
// one. Returns io.EOF at the end-of-archive marker or a clean stream end.
func (w *Reader) Next() (*Entry, error) {
	if w.body != nil && w.body.N > 0 {
		if _, err := io.Copy(io.Discard, w.body); err != nil {
			return nil, err
		}
	}
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(w.r, hdr); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.EOF // short trailing block == end marker on some writers
		}
		return nil, err
	}
	if isAllZero(hdr) {
		return nil, io.EOF
	}
	name := cstr(hdr[:nameLen])
	sizeStr := cstr(hdr[nameLen : nameLen+sizeLen])
	prefix := cstr(hdr[nameLen+sizeLen+mtimeLen:])
	// Some writer versions pad the end-of-archive block with non-zero bytes; an
	// empty file name is the practical EOF signal (seen in real exports).
	if name == "" {
		return nil, io.EOF
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("wpress: bad size %q for %q", sizeStr, name)
	}
	p := path.Clean(path.Join(prefix, name))
	// Jail: a hostile archive must not escape the extraction root.
	if p == "." || strings.HasPrefix(p, "..") || path.IsAbs(p) {
		return nil, fmt.Errorf("wpress: unsafe entry path %q", prefix+"/"+name)
	}
	w.body = &io.LimitedReader{R: w.r, N: size}
	return &Entry{Path: p, Size: size, Body: w.body}, nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func isAllZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// emptyHexRe matches a bare hex literal `0x` NOT followed by a hex digit — the
// empty-blob token All-in-One writes (e.g. Wordfence config: `('key',0x,'yes')`).
// Modern MariaDB rejects a digitless `0x` ("Unknown column '0x'"), so it must
// become an empty string. Consumes the one trailing byte to disambiguate; a real
// `0x3139…` value is left untouched because its next byte IS a hex digit.
var emptyHexRe = regexp.MustCompile(`0x([^0-9A-Fa-f])`)

// SanitizeDump rewrites an All-in-One database.sql so a fresh WordPress on this
// panel can load it via a plain mariadb client. Two fixes, line by line (the
// dump writes one statement per line):
//
//   - the masked table prefix `SERVMASK_PREFIX_` becomes targetPrefix (wp_), so
//     tables match the fresh install's default prefix;
//   - bare `0x` empty-hex literals become '' (newer MariaDB rejects a digitless
//     0x).
//
// Line-oriented so neither the prefix token nor a 0x literal can straddle a read
// boundary. A statement line can be several MB (a wide INSERT), which is fine for
// an ~100 MB dump.
func SanitizeDump(w io.Writer, src io.Reader, targetPrefix string) error {
	br := bufio.NewReaderSize(src, 1<<20)
	tok := []byte("SERVMASK_PREFIX_")
	rep := []byte(targetPrefix)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.ReplaceAll(line, tok, rep)
			line = emptyHexRe.ReplaceAll(line, []byte("''$1"))
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
