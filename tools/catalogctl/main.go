// Command catalogctl validates the catalog and builds its signed index.
//
//	catalogctl validate [flags] [name...]
//	catalogctl build --out DIR [--bin DIR] [flags]
//	catalogctl pending [--previous FILE|URL]
//	catalogctl entries
//	catalogctl keygen
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PublicKey is the catalog key that the daemon embeds.
const PublicKey = "Av6bL0fDX2i7hUF5ioVDsznuNX6OJE8hzMc82/wj7oM="

// BaseURL is the public root of the published catalog.
const BaseURL = "https://catalog.trebi.ai/v1/"

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "catalogctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: catalogctl validate|build|pending|entries|keygen")
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "the repository root")
	schema := fs.String("schema", "", "the manifest JSON Schema (default: <root>/schema/trebi-connector.schema.json)")
	github := fs.String("github-url", "https://github.com", "the GitHub base URL for release assets")
	checkAssets := fs.Bool("check-assets", false, "validate: fail an entry whose github_release assets do not exist")
	out := fs.String("out", "", "build: the output folder")
	previous := fs.String("previous", "", "build, pending: the published index.json, as a file or a URL")
	bin := fs.String("bin", "", "build: the folder of the program archives, as <name>/<bin>_<os>_<arch>.tar.gz")
	baseURL := fs.String("base-url", BaseURL, "build: the public URL of the v1 folder")
	pub := fs.String("public-key", PublicKey, "build: the expected public key of CATALOG_SIGNING_KEY")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *schema == "" {
		*schema = filepath.Join(*root, "schema", "trebi-connector.schema.json")
	}
	fetch := HTTP{Client: &http.Client{Timeout: 5 * time.Minute}}
	rc := ReleaseChecker{Fetch: fetch, GitHubURL: *github}
	switch cmd {
	case "validate":
		entries, err := loadEntries(*root, fs.Args())
		if err != nil {
			return err
		}
		return validate(ctx, entries, *schema, *checkAssets, rc, stdout)
	case "build":
		if *out == "" {
			return errors.New("build: --out is required")
		}
		entries, err := loadEntries(*root, nil)
		if err != nil {
			return err
		}
		if err := validate(ctx, entries, *schema, false, rc, stdout); err != nil {
			return err
		}
		key, err := ParseSigningKey(os.Getenv("CATALOG_SIGNING_KEY"), *pub)
		if err != nil {
			return err
		}
		prev, err := readPrevious(ctx, fetch, *previous)
		if err != nil {
			return err
		}
		b := &Builder{BaseURL: *baseURL, Previous: prev, Fetch: fetch, Bin: *bin, Now: time.Now(), Log: stdout}
		o, err := b.Build(ctx, entries)
		if err != nil {
			return err
		}
		return o.Write(*out, key)
	case "pending":
		entries, err := loadEntries(*root, nil)
		if err != nil {
			return err
		}
		prev, err := readPrevious(ctx, fetch, *previous)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(Pending(entries, prev))
	case "entries":
		// One line per entry: the name, the version, and the program that
		// the catalog builds ("" when it builds none).
		entries, err := loadEntries(*root, nil)
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", e.Manifest.Name, e.Manifest.Version, e.Manifest.CatalogBin())
		}
		return nil
	case "keygen":
		pubKey, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "CATALOG_SIGNING_KEY=%s\npublic key: %s\n",
			base64.StdEncoding.EncodeToString(key.Seed()), base64.StdEncoding.EncodeToString(pubKey))
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// loadEntries reads catalog/<name> for each name, or every entry.
func loadEntries(root string, names []string) ([]*Entry, error) {
	dir := filepath.Join(root, "catalog")
	if len(names) == 0 {
		des, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, d := range des {
			if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
				names = append(names, d.Name())
			}
		}
	}
	slices.Sort(names)
	var out []*Entry
	for _, n := range names {
		e, err := LoadEntry(filepath.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func validate(ctx context.Context, entries []*Entry, schemaPath string, checkAssets bool, rc ReleaseChecker, w io.Writer) error {
	schema, err := LoadSchema(schemaPath)
	if err != nil {
		return err
	}
	bad := 0
	for _, e := range entries {
		errs := e.Validate(schema)
		for _, err := range errs {
			fmt.Fprintf(w, "FAIL %s: %v\n", e.Name(), err)
		}
		if len(errs) > 0 {
			bad++
			continue
		}
		fmt.Fprintf(w, "ok   %s %s\n", e.Name(), e.Manifest.Version)
		if checkAssets {
			if err := rc.Check(ctx, e.Manifest); err != nil {
				fmt.Fprintf(w, "FAIL %s: release assets: %v\n", e.Name(), err)
				bad++
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d entries failed", bad, len(entries))
	}
	return nil
}

// Build is one program archive that the catalog must build.
type Build struct {
	Name    string `json:"name"`
	Bin     string `json:"bin"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Pending lists the archives of each install.catalog entry whose version
// is not in prev.
func Pending(entries []*Entry, prev *Index) []Build {
	out := []Build{}
	for _, e := range entries {
		m := e.Manifest
		if m.CatalogBin() == "" || prev.Has(m.Name, m.Version) {
			continue
		}
		for _, p := range m.Targets() {
			goos, goarch, _ := strings.Cut(p, "/")
			out = append(out, Build{Name: m.Name, Bin: m.CatalogBin(), Version: m.Version, OS: goos, Arch: goarch})
		}
	}
	return out
}

func readPrevious(ctx context.Context, f Fetcher, src string) (*Index, error) {
	if src == "" {
		return nil, nil
	}
	var data []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		data, err = f.Get(ctx, src)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return nil, fmt.Errorf("previous index: %w", err)
	}
	return ReadIndex(data)
}
