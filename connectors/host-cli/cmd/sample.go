// Package cmd has the commands of host-cli.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/probe"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/sshx"
)

func settings(c *cli.Context) config.Settings {
	return c.App.Metadata["settings"].(config.Settings) //nolint:forcetypeassert // Before sets it
}

// SampleCommand returns the `sample` command: it reads the server once.
func SampleCommand() *cli.Command {
	return &cli.Command{
		Name:  "sample",
		Usage: "Read the server once and print the numbers",
		Flags: []cli.Flag{&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "output as JSON"}},
		Action: func(c *cli.Context) error {
			s := settings(c)
			if m := s.Missing(); m != nil {
				return errors.New(m.Error())
			}
			cl, err := sshx.New(s)
			if err != nil {
				return err
			}
			defer cl.Close() //nolint:errcheck // read-only
			out, err := cl.Run(c.Context, probe.Script)
			if err != nil {
				return err
			}
			smp, err := probe.Parse(string(out))
			if err != nil {
				return err
			}
			w := c.App.Writer
			if c.Bool("json") {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					probe.Sample
					Metrics     map[string]float64 `json:"metrics"`
					FailedUnits []string           `json:"failed_units"`
				}{smp, smp.Metrics(), smp.FailedUnits()})
			}
			fmt.Fprintf(w, "%s  CPU %.1f%% of %d  memory %.1f%%  swap %.1f%%  load %.2f %.2f %.2f\n", smp.Hostname, smp.CPU, smp.CPUs, smp.MemUsedPct(), smp.SwapUsedPct(), smp.Load[0], smp.Load[1], smp.Load[2])
			for _, d := range smp.Disks {
				fmt.Fprintf(w, "disk %-20s %5.1f%%  %.1f GiB free\n", d.Mount, d.UsedPct, float64(d.FreeBytes)/(1<<30))
			}
			for _, p := range smp.Processes {
				fmt.Fprintf(w, "proc %7d %-10s %5.1f%% cpu %5.1f%% mem  %s\n", p.PID, p.User, p.CPU, p.Mem, p.Command)
			}
			if f := smp.FailedUnits(); len(f) > 0 {
				fmt.Fprintf(w, "failed services: %s\n", strings.Join(f, ", "))
			}
			return nil
		},
	}
}
