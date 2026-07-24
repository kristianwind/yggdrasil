package api

import (
	"strings"
	"testing"
)

func TestPhpSerializeStrings(t *testing.T) {
	got := phpSerializeStrings([]string{"akismet/akismet.php", "woocommerce/woocommerce.php"})
	want := `a:2:{i:0;s:19:"akismet/akismet.php";i:1;s:27:"woocommerce/woocommerce.php";}`
	if got != want {
		t.Errorf("serialize:\n got %q\nwant %q", got, want)
	}
	if e := phpSerializeStrings(nil); e != "a:0:{}" {
		t.Errorf("empty: got %q", e)
	}
}

func TestWpressOptionsFixSQL(t *testing.T) {
	pkg := []byte(`{"Template":"Divi","Stylesheet":"Divi","Plugins":["woocommerce/woocommerce.php","jetpack/jetpack.php"]}`)
	sql := wpressOptionsFixSQL(pkg)
	for _, want := range []string{
		"INSERT INTO wp_options",
		"'template','Divi'",
		"'stylesheet','Divi'",
		`'active_plugins','a:2:{i:0;s:27:"woocommerce/woocommerce.php";i:1;s:19:"jetpack/jetpack.php";}'`,
		"ON DUPLICATE KEY UPDATE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}

	// No theme/plugins → no SQL.
	if s := wpressOptionsFixSQL([]byte(`{"Template":"","Plugins":[]}`)); s != "" {
		t.Errorf("expected empty SQL, got %q", s)
	}
	// Malformed JSON → no SQL, no panic.
	if s := wpressOptionsFixSQL([]byte(`not json`)); s != "" {
		t.Errorf("expected empty SQL for bad json, got %q", s)
	}
}

func TestSqlEscape(t *testing.T) {
	if got := sqlEscape("o'brien"); got != "o''brien" {
		t.Errorf("quote escape: %q", got)
	}
	if got := sqlEscape(`a\b`); got != `a\\b` {
		t.Errorf("backslash escape: %q", got)
	}
}
