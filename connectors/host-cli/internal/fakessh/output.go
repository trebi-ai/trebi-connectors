package fakessh

import (
	"fmt"
	"strings"
)

// Values set the output of Output.
type Values struct {
	CPU        int  // CPU use in percent, 0 to 100
	DiskPct    int  // use of / in percent
	FailedUnit bool // nginx.service is failed
	NoSystemd  bool
}

// Output returns the output of the probe script of a server with 4 CPUs,
// 8 GiB of memory with 2 GiB free, and two disks.
func Output(v Values) string {
	var b strings.Builder
	b.WriteString("@@host\nweb-1\n")
	b.WriteString("@@stat\ncpu  1000 0 1000 8000 0 0 0 0 0 0\n")
	busy := v.CPU * 10 // of 1000 ticks
	fmt.Fprintf(&b, "@@stat2\ncpu  %d 0 1000 %d 0 0 0 0 0 0\n", 1000+busy, 8000+1000-busy)
	for i := range 4 {
		fmt.Fprintf(&b, "cpu%d 250 0 250 2000 0 0 0 0 0 0\n", i)
	}
	b.WriteString("@@mem\nMemTotal:        8388608 kB\nMemFree:         1048576 kB\nMemAvailable:    2097152 kB\nSwapTotal:       1048576 kB\nSwapFree:         786432 kB\n")
	b.WriteString("@@load\n0.52 0.40 0.31 2/180 4242\n")
	used := 1000000 * v.DiskPct / 100
	fmt.Fprintf(&b, "@@disk\nFilesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/sda1          1000000 %9d %9d %d%% /\n/dev/sdb1          2000000    500000   1500000      25%% /srv/data dir\n", used, 1000000-used, v.DiskPct)
	b.WriteString("@@ps\n 4242 www-data 37.5  2.1 nginx\n  812 postgres 12.0 10.4 postgres\n    1 root      0.0  0.1 systemd\n")
	b.WriteString("@@units\n")
	if !v.NoSystemd {
		state := "active   running"
		if v.FailedUnit {
			state = "failed   failed "
		}
		fmt.Fprintf(&b, "cron.service      loaded    active   running Regular background program processing daemon\nnginx.service     loaded    %s A high performance web server\nauditd.service    not-found inactive dead    auditd.service\n", state)
	}
	b.WriteString("@@end\n")
	return b.String()
}
