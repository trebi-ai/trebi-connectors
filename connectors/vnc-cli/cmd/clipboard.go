package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
)

func (r *Runner) pasteCommand() *cli.Command {
	return &cli.Command{
		Name:      "paste",
		Usage:     "Put text on the clipboard of the remote computer; - reads it from stdin",
		ArgsUsage: "TEXT | - | --file PATH",
		Flags:     []cli.Flag{&cli.StringFlag{Name: "file", Usage: "put the content of this file"}},
		Action: func(ctx context.Context, c *cli.Command) error {
			var text string
			switch {
			case c.String("file") != "" && c.NArg() == 0:
				b, err := os.ReadFile(c.String("file"))
				if err != nil {
					return usage(err)
				}
				text = string(b)
			case c.NArg() == 1 && c.Args().First() == "-":
				b, err := io.ReadAll(r.stdin)
				if err != nil {
					return err
				}
				text = string(b)
			case c.NArg() == 1 && c.String("file") == "":
				text = c.Args().First()
			default:
				return usage(fmt.Errorf("paste needs one of TEXT, -, or --file PATH"))
			}
			return r.do(ctx, paste(strings.ReplaceAll(text, "\r\n", "\n")))
		},
	}
}

func paste(text string) step {
	return func(ctx context.Context, rm remote) error { return rm.Paste(ctx, text) }
}

func (r *Runner) clipboardCommand() *cli.Command {
	return &cli.Command{
		Name:  "clipboard",
		Usage: "Read the clipboard of the remote computer",
		Commands: []*cli.Command{{
			Name:  "get",
			Usage: "Write the last clipboard text of the remote computer",
			Action: func(ctx context.Context, c *cli.Command) error {
				return r.do(ctx, func(ctx context.Context, rm remote) error {
					text, err := rm.Clipboard(ctx)
					if err != nil {
						return err
					}
					if r.json {
						return r.print(map[string]string{"text": text}, "")
					}
					_, err = io.WriteString(r.stdout, text)
					return err
				})
			},
		}},
	}
}
