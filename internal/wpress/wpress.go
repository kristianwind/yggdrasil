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
	"bytes"
	"fmt"
	"io"
	"path"
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

// PrefixReplacer streams src replacing every SERVMASK_PREFIX (the masked table
// prefix All-in-One writes into database.sql) with the real prefix. It carries
// a small tail across chunk boundaries so a token split between reads is still
// caught — a plain chunked replace would silently corrupt exactly those tables.
func PrefixReplacer(src io.Reader, prefix string) io.Reader {
	return &replaceReader{src: src, old: []byte("SERVMASK_PREFIX"), new: []byte(prefix)}
}

type replaceReader struct {
	src  io.Reader
	old  []byte
	new  []byte
	buf  []byte // pending output
	tail []byte // unemitted trailing bytes that could start a token
	done bool
}

func (r *replaceReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 && !r.done {
		chunk := make([]byte, 64<<10)
		n, err := r.src.Read(chunk)
		data := append(r.tail, chunk[:n]...) //nolint:gocritic // fresh slice each round
		if err == io.EOF {
			r.done = true
			r.buf = bytes.ReplaceAll(data, r.old, r.new)
			r.tail = nil
			break
		}
		if err != nil {
			return 0, err
		}
		data = bytes.ReplaceAll(data, r.old, r.new)
		// Hold back len(old)-1 bytes: they may be the start of a split token.
		keep := len(r.old) - 1
		if keep > len(data) {
			keep = len(data)
		}
		r.buf = data[:len(data)-keep]
		r.tail = append([]byte(nil), data[len(data)-keep:]...)
	}
	if len(r.buf) == 0 && r.done {
		return 0, io.EOF
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
