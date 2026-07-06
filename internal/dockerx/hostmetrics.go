package dockerx

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostMetrics is a host-wide resource snapshot for the hardware monitoring page.
// Every field is best-effort: on unsupported platforms or when a sensor is
// absent, the field is zero/empty and the frontend simply hides it.
type HostMetrics struct {
	CPUPercent float64       `json:"cpuPct"`
	MemPercent float64       `json:"memPct"`
	MemUsedMB  float64       `json:"memUsedMb"`
	MemTotalMB float64       `json:"memTotalMb"`
	Load1      float64       `json:"load1"`
	Temp       []TempReading `json:"temp,omitempty"`
}

// TempReading is a single temperature sensor reading, e.g. {Name:"CPU", TempC:62}.
type TempReading struct {
	Name  string  `json:"name"`
	TempC float64 `json:"tempC"`
}

// hostMetricState caches the previous /proc/stat sample so CPU% can be computed as
// a delta between successive reads. A short TTL also caches the computed value so the polling
// frontend (5s) and any concurrent caller don't fork two nvidia-smi/proc reads.
const hostMetricTTL = 2 * time.Second

type hostMetricCache struct {
	mu       sync.Mutex
	at       time.Time
	snapshot HostMetrics
	// CPU delta bookkeeping (raw jiffies from /proc/stat).
	prevIdle uint64
	prevBusy uint64
}

var hostCache = &hostMetricCache{}

// HostMetrics returns a fresh host-wide resource snapshot. The first call seeds the
// CPU delta baseline and returns 0% CPU; subsequent calls report the delta. Reads of
// /proc/stat, /proc/meminfo, /proc/loadavg, and /sys/class/hwmon are all
// best-effort and silent on failure, so this works on Linux and returns a zero-ish
// (non-error) result on other platforms.
func (d *Client) HostMetrics(ctx context.Context) HostMetrics {
	hostCache.mu.Lock()
	if time.Since(hostCache.at) < hostMetricTTL {
		s := hostCache.snapshot
		hostCache.mu.Unlock()
		return s
	}
	hostCache.mu.Unlock()

	m := readHostMetrics()

	hostCache.mu.Lock()
	hostCache.at = time.Now()
	hostCache.snapshot = m
	hostCache.mu.Unlock()
	return m
}

// readHostMetrics performs the actual reads. Separated for testability.
func readHostMetrics() HostMetrics {
	m := HostMetrics{}
	if runtime.GOOS != "linux" {
		// Non-Linux: CPU%/temp sensors aren't available without extra deps. Memory is
		// still useful via the agent runtime, surfaced by SystemInfo instead.
		return m
	}
	m.CPUPercent = readCPUPercent()
	memUsed, memTotal := readMemInfo()
	m.MemUsedMB = round2(memUsed)
	m.MemTotalMB = round2(memTotal)
	if memTotal > 0 {
		m.MemPercent = round2(memUsed / memTotal * 100)
	}
	m.Load1 = readLoadAvg()
	m.Temp = readTemps()
	return m
}

// readCPUPercent computes overall CPU utilisation from /proc/stat since the last
// call. The first call seeds prevIdle/prevBusy and returns 0.
func readCPUPercent() float64 {
	hostCache.mu.Lock()
	prevIdle := hostCache.prevIdle
	prevBusy := hostCache.prevBusy
	hostCache.mu.Unlock()

	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0
	}
	// First line: cpu  user nice system idle iowait irq softirq steal ...
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var vals [10]uint64
	for i := 1; i < len(fields) && i-1 < len(vals); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		vals[i-1] = v
	}
	idle := vals[3] + vals[4]                                         // idle + iowait
	busy := vals[0] + vals[1] + vals[2] + vals[5] + vals[6] + vals[7] // user+nice+system+irq+softirq+steal

	hostCache.mu.Lock()
	hostCache.prevIdle = idle
	hostCache.prevBusy = busy
	hostCache.mu.Unlock()

	totalDelta := (idle - prevIdle) + (busy - prevBusy)
	if totalDelta <= 0 {
		return 0
	}
	pct := float64(busy-prevBusy) / float64(totalDelta) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return round2(pct)
}

// readMemInfo parses /proc/meminfo and returns (usedMB, totalMB). Used = total - avail.
func readMemInfo() (float64, float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var totalKB, availKB uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			totalKB = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			availKB = parseKB(line)
		}
	}
	if totalKB == 0 {
		return 0, 0
	}
	usedKB := totalKB - availKB
	return float64(usedKB) / 1024.0, float64(totalKB) / 1024.0
}

// parseKB extracts the numeric (kB) value from a /proc/meminfo line.
func parseKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseUint(fields[1], 10, 64)
	return n
}

// readLoadAvg returns the 1-minute load average from /proc/loadavg.
func readLoadAvg() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return round2(v)
}

// readTemps reads CPU/mother temperature sensors from /sys/class/hwmon. Each hwmonN
// directory has a name file (e.g. "coretemp") and temp*_input files in millidegrees
// Celsius, optionally with temp*_label. We surface the first usable reading per hwmon
// device, capped to a sane count.
func readTemps() []TempReading {
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return nil
	}
	var out []TempReading
	for _, e := range entries {
		namePath := filepath.Join("/sys/class/hwmon", e.Name(), "name")
		nameData, err := os.ReadFile(namePath)
		if err != nil {
			continue
		}
		devName := strings.TrimSpace(string(nameData))
		dir := filepath.Join("/sys/class/hwmon", e.Name())
		// temp1_input is usually the aggregate/Package reading; prefer it.
		tempC, label := readFirstTemp(dir)
		if tempC > 0 {
			display := devName
			if label != "" {
				display = devName + " (" + label + ")"
			}
			out = append(out, TempReading{Name: display, TempC: tempC})
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// readFirstTemp returns the first valid temperature (in °C) and its label from a
// hwmon directory.
func readFirstTemp(dir string) (float64, string) {
	for i := 1; i <= 8; i++ {
		inPath := filepath.Join(dir, "temp"+strconv.Itoa(i)+"_input")
		raw, err := os.ReadFile(inPath)
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil || milli <= 0 {
			continue
		}
		label := ""
		if lb, err := os.ReadFile(filepath.Join(dir, "temp"+strconv.Itoa(i)+"_label")); err == nil {
			label = strings.TrimSpace(string(lb))
		}
		return round2(milli / 1000.0), label
	}
	return 0, ""
}
