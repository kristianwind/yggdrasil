package api

import "testing"

func TestAutoRestartCron(t *testing.T) {
	cases := []struct {
		hours int
		want  string
	}{
		{0, "0 */1 * * *"}, // clamped up to 1
		{1, "0 */1 * * *"},
		{6, "0 */6 * * *"},
		{12, "0 */12 * * *"},
		{24, "0 0 * * *"}, // daily
		{48, "0 0 * * *"}, // clamped down to daily
	}
	for _, c := range cases {
		if got := autoRestartCron(c.hours); got != c.want {
			t.Errorf("autoRestartCron(%d) = %q, want %q", c.hours, got, c.want)
		}
	}
}

func TestParseAutoRestartHours(t *testing.T) {
	cases := []struct {
		expr string
		want int
	}{
		{"0 */6 * * *", 6},
		{"0 */2 * * *", 2},
		{"0 0 * * *", 24},
		{"garbage", 6}, // fallback
		{"", 6},        // fallback
	}
	for _, c := range cases {
		if got := parseAutoRestartHours(c.expr); got != c.want {
			t.Errorf("parseAutoRestartHours(%q) = %d, want %d", c.expr, got, c.want)
		}
	}
}

// Round-trip: hours → cron → hours is stable for the values the UI offers.
func TestAutoRestartRoundTrip(t *testing.T) {
	for _, h := range []int{1, 2, 3, 4, 6, 8, 12, 24} {
		if got := parseAutoRestartHours(autoRestartCron(h)); got != h {
			t.Errorf("round-trip %d = %d", h, got)
		}
	}
}
