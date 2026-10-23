package cmd

import (
	"context"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"
	"golang.org/x/image/bmp"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

var rectFlag = &cli.StringFlag{Name: "region", Aliases: []string{"r"}, Usage: "only the area X,Y,W,H"}

func (r *Runner) captureCommand() *cli.Command {
	return &cli.Command{
		Name:      "capture",
		Usage:     "Save the screen to a picture (png, jpg, gif, or bmp; - writes PNG to stdout)",
		ArgsUsage: "FILE",
		Flags: []cli.Flag{
			rectFlag,
			&cli.StringFlag{Name: "cursor", Value: string(vnc.CursorNone), Usage: "the pointer in the picture: none, local (drawn by vnc-cli), or server"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() != 1 {
				return usage(fmt.Errorf("capture needs one FILE"))
			}
			area, err := parseRect(c.String("region"))
			if err != nil {
				return err
			}
			switch m := vnc.CursorMode(c.String("cursor")); m {
			case vnc.CursorNone, vnc.CursorLocal, vnc.CursorServer:
				r.opts.Cursor = m
			default:
				return usage(fmt.Errorf("--cursor: %q is not none, local, or server", m))
			}
			return r.do(ctx, r.capture(c.Args().First(), area))
		},
	}
}

func (r *Runner) capture(file string, area image.Rectangle) step {
	return func(ctx context.Context, rm remote) error {
		if err := checkFormat(file); err != nil {
			return err
		}
		img, err := rm.Capture(ctx, area)
		if err != nil {
			return err
		}
		if err := r.saveImage(file, img); err != nil {
			return failed(err)
		}
		if file == "-" {
			return nil
		}
		b := img.Bounds()
		return r.print(map[string]any{"file": file, "width": b.Dx(), "height": b.Dy()}, "")
	}
}

func checkFormat(file string) error {
	if file == "-" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp":
		return nil
	}
	return usage(fmt.Errorf("%s: the picture format is not png, jpg, gif, or bmp", file))
}

func (r *Runner) saveImage(file string, img image.Image) error {
	if file == "-" {
		return png.Encode(r.stdout, img)
	}
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	if err := encodeImage(f, file, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func encodeImage(w io.Writer, file string, img image.Image) error {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".jpg", ".jpeg":
		return jpeg.Encode(w, img, &jpeg.Options{Quality: 95})
	case ".gif":
		return gif.Encode(w, img, nil)
	case ".bmp":
		return bmp.Encode(w, img)
	}
	return png.Encode(w, img)
}

func loadImage(file string) (image.Image, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, usage(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, usage(fmt.Errorf("%s: %w", file, err))
	}
	return img, nil
}

func (r *Runner) infoCommand() *cli.Command {
	return &cli.Command{
		Name:  "info",
		Usage: "Show the name, the size, and the protocol details of the screen",
		Action: func(ctx context.Context, c *cli.Command) error {
			return r.do(ctx, func(ctx context.Context, rm remote) error {
				in, err := rm.Info(ctx)
				if err != nil {
					return err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "name: %s\nsize: %dx%d", in.Name, in.Width, in.Height)
				for _, k := range slices.Sorted(maps.Keys(in.Facts)) {
					if k != "name" {
						fmt.Fprintf(&b, "\n%s: %s", k, in.Facts[k])
					}
				}
				return r.print(in, b.String())
			})
		},
	}
}

// parseRect reads X,Y,W,H. An empty string is the full screen.
func parseRect(s string) (image.Rectangle, error) {
	if s == "" {
		return image.Rectangle{}, nil
	}
	n, err := ints(s, 4)
	if err != nil || n[2] <= 0 || n[3] <= 0 {
		return image.Rectangle{}, usage(fmt.Errorf("region %q is not X,Y,W,H", s))
	}
	return image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3]), nil
}

// parsePoint reads X,Y. An empty string is 0,0.
func parsePoint(s string) (image.Point, error) {
	if s == "" {
		return image.Point{}, nil
	}
	n, err := ints(s, 2)
	if err != nil {
		return image.Point{}, usage(fmt.Errorf("position %q is not X,Y", s))
	}
	return image.Pt(n[0], n[1]), nil
}

func ints(s string, want int) ([]int, error) {
	parts := strings.Split(s, ",")
	if len(parts) != want {
		return nil, fmt.Errorf("want %d numbers", want)
	}
	out := make([]int, want)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 {
			return nil, fmt.Errorf("%q is not a number", p)
		}
		out[i] = v
	}
	return out, nil
}
