// Package sysprobe reads hardware and load facts from the running system.
// All tuning decisions start here: Slipstream never copies a fixed
// "high-performance" config, it calculates one for this machine.
package sysprobe

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Facts is a point-in-time snapshot of the machine.
type Facts struct {
	CPUCount       int
	MemTotalMB     int64
	MemAvailableMB int64
	Load1          float64
	DiskTotalMB    int64
	DiskFreeMB     int64
	UptimeSeconds  int64
}

// Probe collects facts, using dataDir's filesystem for disk numbers.
func Probe(dataDir string) (Facts, error) {
	f := Facts{CPUCount: runtime.NumCPU()}

	total, avail, err := readMeminfo("/proc/meminfo")
	if err != nil {
		return f, err
	}
	f.MemTotalMB, f.MemAvailableMB = total, avail

	f.Load1 = readLoad1("/proc/loadavg")
	f.UptimeSeconds = readUptime("/proc/uptime")

	var st unix.Statfs_t
	if err := unix.Statfs(dataDir, &st); err == nil {
		bs := int64(st.Bsize)
		f.DiskTotalMB = int64(st.Blocks) * bs / (1 << 20)
		f.DiskFreeMB = int64(st.Bavail) * bs / (1 << 20)
	}
	return f, nil
}

// readMeminfo parses MemTotal and MemAvailable (kB) into MB.
func readMeminfo(path string) (totalMB, availMB int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			totalMB = kb / 1024
		case "MemAvailable:":
			availMB = kb / 1024
		}
	}
	return totalMB, availMB, sc.Err()
}

func readLoad1(path string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func readUptime(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return int64(v)
}

// HeadroomPct returns the percentage of a resource still available.
func HeadroomPct(free, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(free * 100 / total)
}
