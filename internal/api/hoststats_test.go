package api

import "testing"

func TestParseMemInfo(t *testing.T) {
	// Trimmed real /proc/meminfo (values in kB).
	sample := []byte(`MemTotal:        8169784 kB
MemFree:          234112 kB
MemAvailable:    5242880 kB
Buffers:          102400 kB
Cached:          2097152 kB
`)
	total, used := parseMemInfo(sample)
	if total != 8169784*1024 {
		t.Fatalf("total = %d, want %d", total, uint64(8169784)*1024)
	}
	// used = MemTotal - MemAvailable = 8169784 - 5242880 = 2926904 kB
	wantUsed := uint64(8169784-5242880) * 1024
	if used != wantUsed {
		t.Fatalf("used = %d, want %d", used, wantUsed)
	}
}

func TestParseMemInfoMissing(t *testing.T) {
	if total, used := parseMemInfo([]byte("Garbage:\n")); total != 0 || used != 0 {
		t.Fatalf("expected 0,0 for unparseable input, got %d,%d", total, used)
	}
}

func TestParseCPUStat(t *testing.T) {
	// cpu  user nice system idle iowait irq softirq steal guest guest_nice
	sample := []byte(`cpu  100 0 50 800 50 0 0 0 0 0
cpu0 50 0 25 400 25 0 0 0 0 0
intr 12345
`)
	idle, total, ok := parseCPUStat(sample)
	if !ok {
		t.Fatal("expected ok")
	}
	// idle = idle(800) + iowait(50) = 850
	if idle != 850 {
		t.Fatalf("idle = %d, want 850", idle)
	}
	// total = sum of all = 100+0+50+800+50 = 1000
	if total != 1000 {
		t.Fatalf("total = %d, want 1000", total)
	}
}

func TestParseCPUStatBusyMath(t *testing.T) {
	// Two samples: between them, total advances 1000, idle advances 700 → 30% busy.
	i1, t1, _ := parseCPUStat([]byte("cpu  100 0 50 800 50 0 0 0\n"))
	i2, t2, _ := parseCPUStat([]byte("cpu  300 0 100 1400 100 0 0 0\n"))
	dt := float64(t2 - t1) // (300+100+1400+100) - (1000) = 1900-1000=900
	di := float64(i2 - i1) // (1400+100) - (850) = 1500-850 = 650
	pct := (1 - di/dt) * 100
	// busy delta = 900-650 = 250 → 250/900 ≈ 27.8%
	if pct < 27 || pct > 29 {
		t.Fatalf("busy pct = %.1f, want ~27.8", pct)
	}
}

func TestParseCPUStatNoCPULine(t *testing.T) {
	if _, _, ok := parseCPUStat([]byte("intr 1\nctxt 2\n")); ok {
		t.Fatal("expected ok=false when no cpu line present")
	}
}
