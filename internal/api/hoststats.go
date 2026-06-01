package api

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// hostMem returns total and used physical RAM in bytes, read from
// /proc/meminfo (Linux/production). On platforms without /proc (e.g. the dev
// Mac) it returns 0,0 and the dashboard simply omits the RAM card.
func hostMem() (total, used uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	return parseMemInfo(data)
}

func parseMemInfo(data []byte) (total, used uint64) {
	var memTotal, memAvail uint64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64) // value is in kB
		switch f[0] {
		case "MemTotal:":
			memTotal = v * 1024
		case "MemAvailable:":
			memAvail = v * 1024
		}
	}
	if memTotal == 0 || memAvail > memTotal {
		return memTotal, 0
	}
	return memTotal, memTotal - memAvail
}

// cpuStat reads the aggregate "cpu" line from /proc/stat and returns the idle
// (idle+iowait) and total jiffies. ok is false when /proc/stat is unavailable.
func cpuStat() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseCPUStat(data)
}

func parseCPUStat(data []byte) (idle, total uint64, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		for i, v := range fields {
			n, _ := strconv.ParseUint(v, 10, 64)
			total += n
			if i == 3 || i == 4 { // idle + iowait
				idle += n
			}
		}
		return idle, total, true
	}
	return 0, 0, false
}

// hostCPUPercent samples /proc/stat twice over a short window and returns the
// busy CPU percentage (0–100). Returns -1 when CPU stats aren't available.
func hostCPUPercent() float64 {
	i1, t1, ok := cpuStat()
	if !ok {
		return -1
	}
	time.Sleep(150 * time.Millisecond)
	i2, t2, ok := cpuStat()
	if !ok || t2 <= t1 {
		return -1
	}
	dt := float64(t2 - t1)
	di := float64(i2 - i1)
	pct := (1 - di/dt) * 100
	if pct < 0 {
		pct = 0
	}
	return pct
}
