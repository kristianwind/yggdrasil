package api

import (
	"strings"
	"testing"
	"time"
)

func TestDetectPlayerAnomaly(t *testing.T) {
	cases := []struct {
		name               string
		prev, cur, histMax int
		wantKind           string
	}{
		{"mass disconnect", 12, 0, 20, "player-drop"},
		{"75pct drop counts", 8, 2, 20, "player-drop"},
		{"small drop ignored", 8, 3, 20, ""},                // 3*4 > 8 — less than 75% gone
		{"few players never a drop", 3, 0, 20, ""},          // below the floor
		{"influx above history", 10, 10, 9, "player-spike"}, // prev==cur, cur > histMax
		{"influx below floor ignored", 7, 7, 5, ""},         // under anomalyPlayerSpikeMin
		{"busy but within history", 15, 15, 20, ""},
		{"no history no spike", 10, 10, -1, ""},
		{"quiet server", 0, 0, 5, ""},
	}
	for _, c := range cases {
		kind, detail := detectPlayerAnomaly(c.prev, c.cur, c.histMax)
		if kind != c.wantKind {
			t.Errorf("%s: got kind %q (detail %q), want %q", c.name, kind, detail, c.wantKind)
		}
		if kind != "" && detail == "" {
			t.Errorf("%s: anomaly without detail", c.name)
		}
	}
}

func TestDetectLogRateSpike(t *testing.T) {
	// Warm-up: never alerts before enough samples, however extreme the count.
	if _, ok := detectLogRateSpike(10000, 10, anomalyLogWarmup-1); ok {
		t.Fatal("alerted during warm-up")
	}
	// Floor: a spike in absolute-quiet logs is noise, not traffic.
	if _, ok := detectLogRateSpike(anomalyLogFloor-1, 1, 100); ok {
		t.Fatal("alerted below the absolute floor")
	}
	// Real spike: 5× the baseline and above the floor.
	d, ok := detectLogRateSpike(500, 50, 100)
	if !ok || !strings.Contains(d, "500") {
		t.Fatalf("expected spike, got ok=%v detail=%q", ok, d)
	}
	// Same volume against a matching baseline: normal traffic, no alert.
	if _, ok := detectLogRateSpike(500, 400, 100); ok {
		t.Fatal("alerted on traffic in line with its own baseline")
	}
	// A zero-ish baseline can't make the factor infinite — the floor still rules.
	if d, ok := detectLogRateSpike(120, 0, 100); !ok || d == "" {
		t.Fatal("floor-clearing burst over a silent baseline should alert")
	}
}

func TestAnomalyStateCooldownAndEMA(t *testing.T) {
	a := newAnomalyState()
	now := time.Now()
	if a.coolingDown("s1", "player-drop", now) {
		t.Fatal("first firing must pass")
	}
	if !a.coolingDown("s1", "player-drop", now.Add(30*time.Minute)) {
		t.Fatal("second firing within the cooldown must be suppressed")
	}
	if a.coolingDown("s1", "log-rate", now) {
		t.Fatal("cooldown must be per kind")
	}
	if !a.coolingDown("s1", "player-drop", now.Add(90*time.Minute)) == false {
		t.Fatal("cooldown must expire")
	}

	// EMA: judged against the baseline from *before* the current observation.
	ema, seen := a.foldLogRate("s1", 100)
	if seen != 0 || ema != 0 {
		t.Fatalf("first sample should see an empty baseline, got ema=%v seen=%d", ema, seen)
	}
	ema, seen = a.foldLogRate("s1", 100)
	if seen != 1 || ema != 100 {
		t.Fatalf("second sample should see the seeded baseline, got ema=%v seen=%d", ema, seen)
	}
}
