// Package probe has the shell script that host-cli runs on a Linux server,
// and the parser of its output.
package probe

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// Script prints the sections that Parse reads. It reads /proc twice, one
// second apart, for the CPU use. It needs no root.
const Script = `export LC_ALL=C
echo '@@host'; hostname
echo '@@stat'; grep '^cpu ' /proc/stat
sleep 1
echo '@@stat2'; grep '^cpu' /proc/stat
echo '@@mem'; cat /proc/meminfo
echo '@@load'; cat /proc/loadavg
echo '@@disk'; df -P -k -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null
echo '@@ps'; ps -eo pid=,user=,pcpu=,pmem=,comm= --sort=-pcpu 2>/dev/null | head -n 10
echo '@@units'; systemctl list-units --type=service --all --no-legend --plain --no-pager 2>/dev/null
echo '@@end'
`

// Sample is one reading of a server.
type Sample struct {
	Hostname  string     `json:"hostname"`
	CPU       float64    `json:"cpu_used_pct"`
	CPUs      int        `json:"cpus"`
	MemTotal  int64      `json:"mem_total_bytes"`
	MemAvail  int64      `json:"mem_available_bytes"`
	SwapTotal int64      `json:"swap_total_bytes"`
	SwapFree  int64      `json:"swap_free_bytes"`
	Load      [3]float64 `json:"load"`
	Disks     []Disk     `json:"disks"`
	Processes []Process  `json:"processes"`
	Units     []Unit     `json:"-"`
	// HasUnits is false when the server has no systemd.
	HasUnits bool `json:"-"`
}

// Disk is one mounted file system.
type Disk struct {
	Mount      string  `json:"mount"`
	Device     string  `json:"device"`
	TotalBytes int64   `json:"total_bytes"`
	FreeBytes  int64   `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// Process is one of the processes with the most CPU use.
type Process struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu_pct"`
	Mem     float64 `json:"mem_pct"`
	Command string  `json:"command"`
}

// Unit is one loaded systemd service.
type Unit struct {
	Name   string `json:"name"`
	Active string `json:"active"`
	Sub    string `json:"sub"`
}

// Failed is true for a unit in the failed state.
func (u Unit) Failed() bool { return u.Active == "failed" }

// MemUsedPct is the share of memory in use.
func (s Sample) MemUsedPct() float64 {
	if s.MemTotal == 0 {
		return 0
	}
	return round(100 * (1 - float64(s.MemAvail)/float64(s.MemTotal)))
}

// SwapUsedPct is the share of swap in use.
func (s Sample) SwapUsedPct() float64 {
	if s.SwapTotal == 0 {
		return 0
	}
	return round(100 * (1 - float64(s.SwapFree)/float64(s.SwapTotal)))
}

// FailedUnits returns the names of the failed units.
func (s Sample) FailedUnits() []string {
	var out []string
	for _, u := range s.Units {
		if u.Failed() {
			out = append(out, u.Name)
		}
	}
	return out
}

// Metrics returns the numbers of the sample as flat keys.
func (s Sample) Metrics() map[string]float64 {
	m := map[string]float64{
		"cpu.used_pct":        s.CPU,
		"cpu.count":           float64(s.CPUs),
		"mem.used_pct":        s.MemUsedPct(),
		"mem.total_bytes":     float64(s.MemTotal),
		"mem.available_bytes": float64(s.MemAvail),
		"swap.used_pct":       s.SwapUsedPct(),
		"load.1":              s.Load[0],
		"load.5":              s.Load[1],
		"load.15":             s.Load[2],
	}
	for _, d := range s.Disks {
		m["disk["+d.Mount+"].used_pct"] = d.UsedPct
		m["disk["+d.Mount+"].free_bytes"] = float64(d.FreeBytes)
	}
	if s.HasUnits {
		m["units.total"] = float64(len(s.Units))
		m["units.failed"] = float64(len(s.FailedUnits()))
	}
	return m
}

// Parse reads the output of Script.
func Parse(out string) (Sample, error) {
	var s Sample
	sections := map[string][]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			cur = name
			sections[cur] = []string{}
			continue
		}
		if cur != "" {
			sections[cur] = append(sections[cur], line)
		}
	}
	if _, ok := sections["end"]; !ok {
		return s, fmt.Errorf("the output of the server is not complete")
	}
	if h := sections["host"]; len(h) > 0 {
		s.Hostname = strings.TrimSpace(h[0])
	}
	s.CPU, s.CPUs = cpu(sections["stat"], sections["stat2"])
	mem(&s, sections["mem"])
	if l := sections["load"]; len(l) > 0 {
		f := strings.Fields(l[0])
		for i := 0; i < 3 && i < len(f); i++ {
			s.Load[i], _ = strconv.ParseFloat(f[i], 64) //nolint:errcheck // 0 on a bad value
		}
	}
	s.Disks = disks(sections["disk"])
	s.Processes = processes(sections["ps"])
	s.Units = units(sections["units"])
	s.HasUnits = len(s.Units) > 0
	return s, nil
}

// cpu returns the CPU use between the two readings and the count of CPUs.
func cpu(a, b []string) (float64, int) {
	n := 0
	for _, l := range b {
		if strings.HasPrefix(l, "cpu") && !strings.HasPrefix(l, "cpu ") {
			n++
		}
	}
	if len(a) == 0 || len(b) == 0 {
		return 0, n
	}
	t1, i1 := cpuTimes(a[0])
	t2, i2 := cpuTimes(b[0])
	if t2 <= t1 {
		return 0, n
	}
	return round(100 * (1 - float64(i2-i1)/float64(t2-t1))), n
}

func cpuTimes(line string) (total, idle int64) {
	f := strings.Fields(line)
	for i := 1; i < len(f) && i <= 8; i++ {
		v, _ := strconv.ParseInt(f[i], 10, 64) //nolint:errcheck // 0 on a bad value
		total += v
		if i == 4 || i == 5 { // idle and iowait
			idle += v
		}
	}
	return total, idle
}

func mem(s *Sample, lines []string) {
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64) //nolint:errcheck // 0 on a bad value
		n *= 1024
		switch k {
		case "MemTotal":
			s.MemTotal = n
		case "MemAvailable":
			s.MemAvail = n
		case "SwapTotal":
			s.SwapTotal = n
		case "SwapFree":
			s.SwapFree = n
		}
	}
}

func disks(lines []string) []Disk {
	var out []Disk
	for i, l := range lines {
		f := strings.Fields(l)
		if i == 0 || len(f) < 6 {
			continue // the header
		}
		used, err1 := strconv.ParseInt(f[2], 10, 64)
		avail, err2 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil || used+avail == 0 {
			continue
		}
		out = append(out, Disk{
			Mount:      strings.Join(f[5:], " "),
			Device:     f[0],
			TotalBytes: (used + avail) * 1024,
			FreeBytes:  avail * 1024,
			UsedPct:    round(100 * float64(used) / float64(used+avail)),
		})
	}
	return out
}

func processes(lines []string) []Process {
	var out []Process
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		c, _ := strconv.ParseFloat(f[2], 64) //nolint:errcheck // 0 on a bad value
		m, _ := strconv.ParseFloat(f[3], 64) //nolint:errcheck // 0 on a bad value
		out = append(out, Process{PID: pid, User: f[1], CPU: c, Mem: m, Command: strings.Join(f[4:], " ")})
	}
	return out
}

func units(lines []string) []Unit {
	var out []Unit
	for _, l := range lines {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "●"))
		if len(f) < 4 || f[1] != "loaded" {
			continue
		}
		out = append(out, Unit{Name: f[0], Active: f[2], Sub: f[3]})
	}
	return out
}

func round(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
