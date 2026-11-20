package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/spec"
)

// downloader saves attachments or assets. The v2 API gives only metadata, so
// the file comes from the v1 detail endpoint.
type downloader struct {
	noun  string
	usage string
	list  *spec.Endpoint // nil: ids are required
	get   spec.Endpoint
	file  spec.Endpoint
}

func newDownloader(s *spec.Spec, noun, path string, listAll bool) downloader {
	get := findEndpoint(s, path+" get")
	file := get
	file.Version = "v1"
	file.Path = strings.Replace(get.Path, "/api/v2/", "/api/v1/", 1)
	d := downloader{noun: noun, get: get, file: file}
	if listAll {
		list := findEndpoint(s, path+" list")
		d.list = &list
	}
	return d
}

type downloadable struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsUploaded bool   `json:"is_uploaded"`
}

func (d downloader) parents() []string {
	pos := positional(d.get)
	var out []string
	for _, n := range pos[:len(pos)-1] {
		out = append(out, "<"+argName(n)+">")
	}
	return out
}

func (d downloader) argsUsage() string {
	ids := "<id>..."
	if d.list != nil {
		ids = "[<id>...]"
	}
	return strings.Join(append(d.parents(), ids), " ")
}

func (d downloader) Command() *cli.Command {
	desc := "Saves each " + d.noun + " under its own name in --dir."
	if d.list != nil {
		desc += " Without ids, downloads all uploaded " + d.noun + "s."
	}
	return &cli.Command{
		Name:        "download",
		Usage:       d.usage,
		ArgsUsage:   d.argsUsage(),
		Description: desc + "\nUse --out to set the file path of one " + d.noun + " (\"-\" for stdout).",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Aliases: []string{"d"}, Value: ".", Usage: "Directory for the files"},
			&cli.StringFlag{Name: "out", Aliases: []string{"O"}, Usage: "File path for one " + d.noun + ", or - for stdout"},
			&cli.BoolFlag{Name: "force", Aliases: []string{"f"}, Usage: "Overwrite files that exist"},
		},
		Action: d.run,
	}
}

func (d downloader) run(ctx context.Context, c *cli.Command) error {
	a := FromCmd(c)
	args := c.Args().Slice()
	np := len(d.parents())
	if len(args) < np || (d.list == nil && len(args) == np) {
		return fmt.Errorf("expected arguments: %s", d.argsUsage())
	}
	parents, ids := args[:np:np], args[np:]
	out := c.String("out")
	cl, err := a.Client()
	if err != nil {
		return err
	}
	files, err := d.records(ctx, a, cl, parents, ids)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no uploaded %ss", d.noun)
	}
	if out != "" && len(files) > 1 {
		return fmt.Errorf("--out takes one %s, got %d: pass an id or use --dir", d.noun, len(files))
	}

	var saved []json.RawMessage
	for _, f := range files {
		path, err := resolvePath(ctx, a, cl, d.file, append(parents, f.ID))
		if err != nil {
			return err
		}
		if out == "-" {
			_, err := cl.Download(ctx, path, c.Root().Writer)
			return err
		}
		dest := out
		if dest == "" {
			dest = filepath.Join(c.String("dir"), filepath.Base(f.Name))
		}
		n, err := save(ctx, cl, path, dest, c.Bool("force"))
		if err != nil {
			return fmt.Errorf("download %s (%s): %w", f.Name, f.ID, err)
		}
		successf(a, os.Stderr, "Downloaded %s (%d bytes) to %s", f.Name, n, dest)
		rec, _ := json.Marshal(map[string]any{"id": f.ID, "name": f.Name, "size": n, "path": dest})
		saved = append(saved, rec)
	}
	if a.JSON() {
		return a.RenderItems(c.Root().Writer, saved)
	}
	return nil
}

// records reads the named records, or all uploaded ones when ids is empty.
func (d downloader) records(ctx context.Context, a *AppContext, cl *client.Client, parents, ids []string) ([]downloadable, error) {
	if len(ids) > 0 {
		var out []downloadable
		for _, id := range ids {
			path, err := resolvePath(ctx, a, cl, d.get, append(parents, id))
			if err != nil {
				return nil, err
			}
			var f downloadable
			if err := cl.GetJSON(ctx, path, nil, &f); err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, nil
	}
	path, err := resolvePath(ctx, a, cl, *d.list, parents)
	if err != nil {
		return nil, err
	}
	items, err := cl.ListAll(ctx, path, nil, true, 0)
	if err != nil {
		return nil, err
	}
	var out []downloadable
	for _, raw := range items {
		var f downloadable
		if json.Unmarshal(raw, &f) == nil && f.IsUploaded {
			out = append(out, f)
		}
	}
	return out, nil
}

// save writes to a temporary file next to dest, then renames it, so a failed
// transfer leaves no partial file.
func save(ctx context.Context, cl *client.Client, path, dest string, force bool) (int64, error) {
	if _, err := os.Stat(dest); err == nil && !force {
		return 0, fmt.Errorf("%s exists (use --force to overwrite)", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.part")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := cl.Download(ctx, path, tmp)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	return n, os.Rename(tmp.Name(), dest)
}
