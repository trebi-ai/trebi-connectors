package main

import (
	"context"
	"fmt"
	"os"

	cli "github.com/urfave/cli/v3"
)

var version = "dev"

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "vnc-cli",
		Usage:   "Control a remote computer over VNC: capture the screen, type, click, and watch for changes",
		Version: version,
	}
}
