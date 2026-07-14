package api

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestAutoRestartCron(t *testing.T) {
	cases := []struct {
		hours, anchor int
		want          string
	}{
		{0, 0, "0 */1 * * *"}, // clamped up to 1
		{1, 0, "0 */1 * * *"},
		{6, 0, "0 */6 * * *"},
		{12, 0, "0 */12 * * *"},
		{24, 0, "0 0 * * *"}, // daily at midnight
		{48, 0, "0 0 * * *"}, // clamped down to daily
		{6, 3, "0 3-23/6 * * *"},
		{4, 23, "0 23-23/4 * * *"}, // a late anchor is legal; it just fires once a day
		{24, 5, "0 5 * * *"},       // daily at 05:00
		{6, -1, "0 */6 * * *"},     // out-of-range anchor falls back to midnight
		{6, 24, "0 */6 * * *"},
	}
	for _, c := range cases {
		if got := autoRestartCron(c.hours, c.anchor); got != c.want {
			t.Errorf("autoRestartCron(%d, %d) = %q, want %q", c.hours, c.anchor, got, c.want)
		}
	}
}

func TestParseAutoRestart(t *testing.T) {
	cases := []struct {
		expr                  string
		wantHours, wantAnchor int
	}{
		{"0 */6 * * *", 6, 0},
		{"0 */2 * * *", 2, 0},
		{"0 0 * * *", 24, 0},
		{"0 3-23/6 * * *", 6, 3},
		{"0 5 * * *", 24, 5},
		{"garbage", 6, 0}, // fallback
		{"", 6, 0},        // fallback
		{"0 */x * * *", 6, 0},
		{"0 a-23/6 * * *", 6, 0},
	}
	for _, c := range cases {
		h, a := parseAutoRestart(c.expr)
		if h != c.wantHours || a != c.wantAnchor {
			t.Errorf("parseAutoRestart(%q) = (%d, %d), want (%d, %d)", c.expr, h, a, c.wantHours, c.wantAnchor)
		}
	}
}

// Round-trip: (hours, anchor) → cron → (hours, anchor) is stable for everything
// the UI can produce. A drift here would silently move a server's restart time.
func TestAutoRestartRoundTrip(t *testing.T) {
	for _, h := range []int{1, 2, 3, 4, 6, 8, 12, 24} {
		for a := 0; a < 24; a++ {
			gotH, gotA := parseAutoRestart(autoRestartCron(h, a))
			if gotH != h {
				t.Errorf("round-trip hours (%d,%d) = %d", h, a, gotH)
			}
			// Sub-daily cycles anchored at 0 are emitted as */N, which carries no
			// anchor — that's the intended equivalence, not a loss.
			if gotA != a {
				t.Errorf("round-trip anchor (%d,%d) = %d", h, a, gotA)
			}
		}
	}
}

// The whole point of the anchor is that the scheduler actually fires at the hour
// the operator picked. Assert against the real cron parser rather than trusting
// the expression to mean what it looks like it means.
func TestAutoRestartFiresAtAnchor(t *testing.T) {
	p := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cases := []struct {
		hours, anchor int
		want          []int
	}{
		{6, 3, []int{3, 9, 15, 21}},
		{6, 0, []int{6, 12, 18, 0}},
		{12, 4, []int{4, 16, 4, 16}},
		{24, 5, []int{5, 5, 5, 5}},
		{8, 2, []int{2, 10, 18, 2}},
	}
	for _, c := range cases {
		expr := autoRestartCron(c.hours, c.anchor)
		sch, err := p.Parse(expr)
		if err != nil {
			t.Fatalf("autoRestartCron(%d,%d) = %q, which cron rejects: %v", c.hours, c.anchor, expr, err)
		}
		tm := time.Date(2026, 7, 14, 0, 0, 0, 0, time.Local)
		var got []int
		for i := 0; i < len(c.want); i++ {
			tm = sch.Next(tm)
			got = append(got, tm.Hour())
			if tm.Minute() != 0 {
				t.Errorf("%q fired at minute %d, want 0", expr, tm.Minute())
			}
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%q fires at hours %v, want %v", expr, got, c.want)
				break
			}
		}
	}
}

// Every (hours, anchor) the UI can produce must parse. An expression the cron
// parser rejects is stored happily and then simply never fires — the server just
// silently stops restarting, with nothing to notice.
func TestAutoRestartEveryCombinationIsValidCron(t *testing.T) {
	p := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, h := range []int{1, 2, 3, 4, 6, 8, 12, 24} {
		for a := 0; a < 24; a++ {
			expr := autoRestartCron(h, a)
			sch, err := p.Parse(expr)
			if err != nil {
				t.Fatalf("autoRestartCron(%d,%d) = %q, which cron rejects: %v", h, a, expr, err)
			}
			// It must also actually fire, and the anchor hour must be one of the
			// firings. Next() is strictly-after, so ask from a minute before the
			// anchor — asking from the anchor itself would skip it.
			tm := time.Date(2026, 7, 14, a, 0, 0, 0, time.Local).Add(-time.Minute)
			next := sch.Next(tm)
			if next.IsZero() {
				t.Errorf("autoRestartCron(%d,%d) = %q never fires", h, a, expr)
				continue
			}
			if next.Hour() != a {
				t.Errorf("autoRestartCron(%d,%d) = %q does not fire at its anchor: next after %v is %02d:00, want %02d:00",
					h, a, expr, tm.Format("15:04"), next.Hour(), a)
			}
		}
	}
}

func TestAutoRestartName(t *testing.T) {
	cases := []struct {
		hours, anchor int
		want          string
	}{
		{6, 0, "Auto-restart every 6h"},
		{6, 3, "Auto-restart every 6h from 03:00"},
		{24, 0, "Auto-restart daily at 00:00"},
		{24, 5, "Auto-restart daily at 05:00"},
	}
	for _, c := range cases {
		if got := autoRestartName(c.hours, c.anchor); got != c.want {
			t.Errorf("autoRestartName(%d,%d) = %q, want %q", c.hours, c.anchor, got, c.want)
		}
	}
}
