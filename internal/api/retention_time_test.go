package api

import (
	"testing"
	"time"
)

// TestParseDBTimeAcceptsSQLiteDefault is the regression guard for the retention
// bug: created_at is written by SQLite's `datetime('now')` schema default, which
// is not RFC3339. Parsing it as RFC3339 yielded the zero time, which both
// retention rules read as "infinitely old" — so a keep-days policy deleted every
// backup, including one taken seconds earlier.
func TestParseDBTimeAcceptsSQLiteDefault(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			// Exactly what `datetime('now')` writes.
			name: "sqlite datetime('now')",
			in:   "2026-07-14 03:15:00",
			want: time.Date(2026, 7, 14, 3, 15, 0, 0, time.UTC),
		},
		{
			name: "sqlite with fractional seconds",
			in:   "2026-07-14 03:15:00.123",
			want: time.Date(2026, 7, 14, 3, 15, 0, 123000000, time.UTC),
		},
		{
			name: "rfc3339",
			in:   "2026-07-14T03:15:00Z",
			want: time.Date(2026, 7, 14, 3, 15, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDBTime(tc.in)
			if err != nil {
				t.Fatalf("parseDBTime(%q) returned error: %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseDBTime(%q) = %v, want %v", tc.in, got, tc.want)
			}
			if got.IsZero() {
				t.Errorf("parseDBTime(%q) returned the zero time — retention would treat this backup as ancient", tc.in)
			}
		})
	}
}

// A timestamp we cannot read must surface as an error, never as a zero time.
// Callers fail closed on the error; a zero time would silently delete data.
func TestParseDBTimeFailsClosed(t *testing.T) {
	for _, in := range []string{"", "not a time", "14/07/2026"} {
		got, err := parseDBTime(in)
		if err == nil {
			t.Errorf("parseDBTime(%q) = %v, want an error", in, got)
		}
	}
}
