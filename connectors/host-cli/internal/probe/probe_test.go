package probe_test

import (
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/fakessh"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/probe"
)

func TestParse(t *testing.T) {
	s, err := probe.Parse(fakessh.Output(fakessh.Values{CPU: 42, DiskPct: 91, FailedUnit: true}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Hostname != "web-1" || s.CPU != 42 || s.CPUs != 4 || s.MemUsedPct() != 75 || s.SwapUsedPct() != 25 || s.Load != [3]float64{0.52, 0.40, 0.31} {
		t.Fatalf("%+v", s)
	}
	if len(s.Disks) != 2 || s.Disks[0].Mount != "/" || s.Disks[0].UsedPct != 91 || s.Disks[1].Mount != "/srv/data dir" || s.Disks[1].FreeBytes != 1500000*1024 {
		t.Fatalf("%+v", s.Disks)
	}
	if len(s.Processes) != 3 || s.Processes[0].PID != 4242 || s.Processes[0].Command != "nginx" || s.Processes[0].CPU != 37.5 {
		t.Fatalf("%+v", s.Processes)
	}
	if len(s.Units) != 2 || len(s.FailedUnits()) != 1 || s.FailedUnits()[0] != "nginx.service" {
		t.Fatalf("%+v", s.Units)
	}
	m := s.Metrics()
	if m["cpu.used_pct"] != 42 || m["disk[/].used_pct"] != 91 || m["units.failed"] != 1 || m["mem.total_bytes"] != 8<<30 || m["load.15"] != 0.31 {
		t.Fatalf("%v", m)
	}
}

func TestParseNoSystemd(t *testing.T) {
	s, err := probe.Parse(fakessh.Output(fakessh.Values{NoSystemd: true}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Metrics()["units.failed"]; ok || s.HasUnits {
		t.Fatalf("%+v", s)
	}
}

func TestParseCut(t *testing.T) {
	if _, err := probe.Parse("@@host\nx\n@@stat\n"); err == nil {
		t.Fatal("want an error for output with no end")
	}
}
