package wpress

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// buildEntry serializes one record the way All-in-One writes it.
func buildEntry(name, prefix, content string) []byte {
	hdr := make([]byte, headerLen)
	copy(hdr[:nameLen], name)
	copy(hdr[nameLen:nameLen+sizeLen], []byte(itoa(len(content))))
	copy(hdr[nameLen+sizeLen:nameLen+sizeLen+mtimeLen], "1700000000")
	copy(hdr[nameLen+sizeLen+mtimeLen:], prefix)
	return append(hdr, content...)
}

func itoa(n int) string {
	return string([]byte(strings.TrimSpace((func() string { b := make([]byte, 0, 14); return string(appendInt(b, n)) })())))
}

func appendInt(b []byte, n int) []byte {
	if n == 0 {
		return append(b, '0')
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return append(b, digits...)
}

func TestReaderWalksArchive(t *testing.T) {
	var arc bytes.Buffer
	arc.Write(buildEntry("package.json", ".", `{"SiteURL":"https://old.example"}`))
	arc.Write(buildEntry("style.css", "themes/mytheme", "body{}"))
	arc.Write(buildEntry("database.sql", ".", "CREATE TABLE `SERVMASK_PREFIX_posts` (id int);\n"))
	arc.Write(make([]byte, headerLen)) // EOF marker

	r := NewReader(&arc)
	var got []string
	var dbBody string
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		got = append(got, e.Path)
		if e.Path == "database.sql" {
			var sb bytes.Buffer
			SanitizeDump(&sb, e.Body, "wp_") //nolint:errcheck
			dbBody = sb.String()
		}
	}
	want := []string{"package.json", "themes/mytheme/style.css", "database.sql"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths: got %v want %v", got, want)
	}
	if dbBody != "CREATE TABLE `wp_posts` (id int);\n" {
		t.Fatalf("prefix not replaced: %q", dbBody)
	}
}

func TestSanitizeDump(t *testing.T) {
	in := "INSERT INTO `SERVMASK_PREFIX_wfconfig` VALUES ('a',0x3139,'yes');\n" +
		"INSERT INTO `SERVMASK_PREFIX_wfconfig` VALUES ('b',0x,'yes');\n" +
		"INSERT INTO `SERVMASK_PREFIX_wfconfig` VALUES ('c',0x,0x,'yes');\n"
	var out bytes.Buffer
	if err := SanitizeDump(&out, strings.NewReader(in), "wp_"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// Prefix restored to a single underscore, real hex kept, bare 0x → '' (incl. consecutive).
	want := "INSERT INTO `wp_wfconfig` VALUES ('a',0x3139,'yes');\n" +
		"INSERT INTO `wp_wfconfig` VALUES ('b','','yes');\n" +
		"INSERT INTO `wp_wfconfig` VALUES ('c','','','yes');\n"
	if got != want {
		t.Fatalf("sanitize wrong:\n got: %q\nwant: %q", got, want)
	}
}

func TestReaderSkipsUnreadBodies(t *testing.T) {
	var arc bytes.Buffer
	arc.Write(buildEntry("a.txt", ".", "AAAA"))
	arc.Write(buildEntry("b.txt", ".", "BBBB"))
	arc.Write(make([]byte, headerLen))
	r := NewReader(&arc)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	// Don't read a's body; Next must drain it and still land correctly on b.
	e, err := r.Next()
	if err != nil || e.Path != "b.txt" {
		t.Fatalf("expected b.txt, got %v %v", e, err)
	}
	b, _ := io.ReadAll(e.Body)
	if string(b) != "BBBB" {
		t.Fatalf("body misaligned: %q", b)
	}
}

func TestReaderRejectsTraversal(t *testing.T) {
	var arc bytes.Buffer
	arc.Write(buildEntry("evil.php", "../../etc", "x"))
	r := NewReader(&arc)
	if _, err := r.Next(); err == nil {
		t.Fatal("traversal entry accepted")
	}
}

func TestSanitizeLargeLine(t *testing.T) {
	// A statement line far larger than the reader buffer must sanitize whole.
	pad := strings.Repeat("x", (1<<20)+5000)
	in := "INSERT INTO `SERVMASK_PREFIX_posts` VALUES ('" + pad + "',0x,'y');\n"
	var out bytes.Buffer
	SanitizeDump(&out, strings.NewReader(in), "wp_") //nolint:errcheck
	if !strings.Contains(out.String(), "`wp_posts`") || !strings.Contains(out.String(), ",'','y')") {
		t.Fatalf("large line not sanitized (len %d)", out.Len())
	}
}
