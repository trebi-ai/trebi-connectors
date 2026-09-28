package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Index is index.json.
type Index struct {
	GeneratedAt string       `json:"generated_at"`
	Entries     []IndexEntry `json:"entries"`
}

// IndexEntry is one connector in the index.
type IndexEntry struct {
	Name         string         `json:"name"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Publisher    string         `json:"publisher"`
	Capabilities []string       `json:"capabilities"`
	IconURL      string         `json:"icon_url,omitempty"`
	Latest       string         `json:"latest"`
	Versions     []IndexVersion `json:"versions"`
}

// IndexVersion is one published snapshot.
type IndexVersion struct {
	Version     string        `json:"version"`
	Digest      string        `json:"digest"`
	URL         string        `json:"url"`
	PublishedAt string        `json:"published_at"`
	Yanked      bool          `json:"yanked,omitempty"`
	Binaries    []IndexBinary `json:"binaries,omitempty"`
}

// IndexBinary is the program archive of one version for one platform.
type IndexBinary struct {
	Platform string `json:"platform"` // "<os>/<arch>"
	URL      string `json:"url"`
	Digest   string `json:"digest"`
}

// BinaryFile is the archive name of one program for one platform.
func BinaryFile(bin, platform string) string {
	return bin + "_" + strings.Replace(platform, "/", "_", 1) + ".tar.gz"
}

// Digest is the digest form of the index: "sha256:<hex>".
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Fetcher reads a URL. The HTTP client implements it.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// Builder builds the snapshots and the index of the catalog.
type Builder struct {
	BaseURL  string // ends with "/v1/"
	Previous *Index // the published index; nil for the first publish
	Fetch    Fetcher
	Bin      string // holds <name>/<BinaryFile> for each new install.catalog version
	Now      time.Time
	Log      io.Writer
}

// Output is what Build writes under the out folder.
type Output struct {
	Index     []byte
	Snapshots map[string][]byte // "snapshots/<name>/<version>.tar.gz"
	Binaries  map[string][]byte // "bin/<name>/<version>/<BinaryFile>"
	Icons     map[string][]byte // "icons/<name>.svg"
}

// Build adds the current version of each entry to the previous index. A
// version that the previous index has must have the same content, or Build
// fails. A new version with install.catalog needs an archive in b.Bin for
// each of its platforms.
func (b *Builder) Build(ctx context.Context, entries []*Entry) (*Output, error) {
	out := &Output{Snapshots: map[string][]byte{}, Binaries: map[string][]byte{}, Icons: map[string][]byte{}}
	byName := map[string]IndexEntry{}
	if b.Previous != nil {
		for _, ie := range b.Previous.Entries {
			byName[ie.Name] = ie
		}
	}
	for _, e := range entries {
		m := e.Manifest
		snap, err := e.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Name, err)
		}
		key := "snapshots/" + m.Name + "/" + m.Version + ".tar.gz"
		ie := byName[m.Name]
		ie.Name, ie.Title, ie.Description = m.Name, m.Title, m.Description
		ie.Publisher = cmpOr(m.Publisher, "community")
		ie.Capabilities = m.Capabilities()
		ie.IconURL = ""
		if m.Icon != "" && strings.HasSuffix(m.Icon, ".svg") {
			icon, err := os.ReadFile(filepath.Join(e.Dir, m.Icon))
			if err != nil {
				return nil, err
			}
			out.Icons["icons/"+m.Name+".svg"] = icon
			ie.IconURL = b.BaseURL + "icons/" + m.Name + ".svg"
		}
		i := slices.IndexFunc(ie.Versions, func(v IndexVersion) bool { return v.Version == m.Version })
		if i >= 0 {
			old := ie.Versions[i]
			if old.Digest != Digest(snap) {
				same, err := b.sameContent(ctx, old, e)
				if err != nil {
					return nil, fmt.Errorf("%s %s: %w", m.Name, m.Version, err)
				}
				if !same {
					return nil, fmt.Errorf("%s %s is published with other content; bump the version", m.Name, m.Version)
				}
				fmt.Fprintf(b.Log, "keep %s %s (same content, other gzip bytes)\n", m.Name, m.Version)
			}
		} else {
			bins, err := b.binaries(m, out)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", m.Name, m.Version, err)
			}
			ie.Versions = append(ie.Versions, IndexVersion{
				Version: m.Version, Digest: Digest(snap), URL: b.BaseURL + key,
				PublishedAt: b.Now.UTC().Format(time.RFC3339), Binaries: bins,
			})
			out.Snapshots[key] = snap
			fmt.Fprintf(b.Log, "add %s %s %s\n", m.Name, m.Version, Digest(snap))
		}
		byName[m.Name] = ie
	}
	idx := Index{GeneratedAt: b.Now.UTC().Format(time.RFC3339), Entries: []IndexEntry{}}
	for _, name := range slices.Sorted(mapKeys(byName)) {
		ie := byName[name]
		slices.SortFunc(ie.Versions, func(x, y IndexVersion) int { return CompareSemver(x.Version, y.Version) })
		ie.Latest = latest(ie.Versions)
		idx.Entries = append(idx.Entries, ie)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	out.Index = append(data, '\n')
	return out, nil
}

// binaries reads the program archives of a new version into out.
func (b *Builder) binaries(m Manifest, out *Output) ([]IndexBinary, error) {
	bin := m.CatalogBin()
	if bin == "" {
		return nil, nil
	}
	var bins []IndexBinary
	for _, p := range m.Targets() {
		file := BinaryFile(bin, p)
		data, err := os.ReadFile(filepath.Join(b.Bin, m.Name, file))
		if err != nil {
			return nil, fmt.Errorf("no program archive for %s; build connectors/%s first: %w", p, bin, err)
		}
		key := "bin/" + m.Name + "/" + m.Version + "/" + file
		out.Binaries[key] = data
		bins = append(bins, IndexBinary{Platform: p, URL: b.BaseURL + key, Digest: Digest(data)})
	}
	return bins, nil
}

// sameContent compares the tar stream of the published snapshot with the
// entry. A new Go version can change the gzip bytes of the same tar.
func (b *Builder) sameContent(ctx context.Context, v IndexVersion, e *Entry) (bool, error) {
	if b.Fetch == nil {
		return false, nil
	}
	pub, err := b.Fetch.Get(ctx, v.URL)
	if err != nil {
		return false, err
	}
	if Digest(pub) != v.Digest {
		return false, fmt.Errorf("the published snapshot does not match its digest %s", v.Digest)
	}
	zr, err := gzip.NewReader(bytes.NewReader(pub))
	if err != nil {
		return false, err
	}
	pubTar, err := io.ReadAll(zr)
	if err != nil {
		return false, err
	}
	cur, err := e.Tar()
	if err != nil {
		return false, err
	}
	return bytes.Equal(pubTar, cur), nil
}

// latest is the highest version that is not yanked. A pre-release counts
// only when there is no release.
func latest(vs []IndexVersion) string {
	best, bestPre := "", ""
	for _, v := range vs {
		if v.Yanked {
			continue
		}
		if strings.Contains(strings.SplitN(v.Version, "+", 2)[0], "-") {
			bestPre = v.Version
		} else {
			best = v.Version
		}
	}
	return cmpOr(best, bestPre)
}

// Write writes the output under dir/v1 and signs the index with key.
func (o *Output) Write(dir string, key ed25519.PrivateKey) error {
	root := filepath.Join(dir, "v1")
	files := map[string][]byte{"index.json": o.Index, "index.json.sig": []byte(Sign(key, o.Index))}
	for k, v := range o.Snapshots {
		files[k] = v
	}
	for k, v := range o.Binaries {
		files[k] = v
	}
	for k, v := range o.Icons {
		files[k] = v
	}
	for k, v := range files {
		p := filepath.Join(root, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, v, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Sign returns the base64 std ed25519 signature of data.
func Sign(key ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
}

// ParseSigningKey reads a base64 std ed25519 seed and checks it against the
// expected base64 std public key.
func ParseSigningKey(seed, wantPub string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seed))
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	if len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key: want a %d-byte seed, got %d bytes", ed25519.SeedSize, len(raw))
	}
	key := ed25519.NewKeyFromSeed(raw)
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	if wantPub != "" && pub != wantPub {
		return nil, fmt.Errorf("signing key: its public key %s is not the expected %s", pub, wantPub)
	}
	return key, nil
}

// CompareSemver orders two semver strings. Build metadata orders after
// the plain version, so 1.3.0+2 follows 1.3.0.
func CompareSemver(a, b string) int {
	ac, am, _ := strings.Cut(a, "+")
	bc, bm, _ := strings.Cut(b, "+")
	an, ap, _ := strings.Cut(ac, "-")
	bn, bp, _ := strings.Cut(bc, "-")
	as, bs := strings.Split(an, "."), strings.Split(bn, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(at(as, i))
		y, _ := strconv.Atoi(at(bs, i))
		if x != y {
			return cmpInt(x, y)
		}
	}
	switch {
	case ap == "" && bp != "":
		return 1
	case ap != "" && bp == "":
		return -1
	case ap != bp:
		return comparePre(ap, bp)
	}
	return strings.Compare(am, bm)
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, xerr := strconv.Atoi(as[i])
		y, yerr := strconv.Atoi(bs[i])
		switch {
		case xerr == nil && yerr == nil && x != y:
			return cmpInt(x, y)
		case xerr == nil && yerr != nil:
			return -1
		case xerr != nil && yerr == nil:
			return 1
		case as[i] != bs[i]:
			return strings.Compare(as[i], bs[i])
		}
	}
	return cmpInt(len(as), len(bs))
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "0"
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Has reports whether the index lists the version of the connector. A nil
// index has nothing.
func (idx *Index) Has(name, version string) bool {
	if idx == nil {
		return false
	}
	for _, e := range idx.Entries {
		if e.Name == name {
			return slices.ContainsFunc(e.Versions, func(v IndexVersion) bool { return v.Version == version })
		}
	}
	return false
}

// ReadIndex reads an index. An empty input is no index.
func ReadIndex(data []byte) (*Index, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("previous index: %w", err)
	}
	if idx.Entries == nil {
		return nil, errors.New("previous index: no entries field")
	}
	return &idx, nil
}
