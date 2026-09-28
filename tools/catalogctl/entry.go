package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

// ManifestFile is the manifest name in an entry folder.
const ManifestFile = "trebi-connector.yaml"

// ProtocolConnector is the events protocol of an adapter process.
const ProtocolConnector = "trebi-connector/1"

// maxEntrySize bounds the files of one entry.
const maxEntrySize = 5 << 20

var (
	templateRE = regexp.MustCompile(`\{[^}]*\}`)
	semverRE   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// Manifest holds the manifest fields that the tool reads. The JSON Schema
// checks the full document.
type Manifest struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Icon        string   `yaml:"icon"`
	Version     string   `yaml:"version"`
	Publisher   string   `yaml:"publisher"`
	Platforms   []string `yaml:"platforms"`
	Install     *Install `yaml:"install"`
	Setup       *struct {
		Login []string `yaml:"login"`
	} `yaml:"setup"`
	Skill *struct {
		Path string `yaml:"path"`
	} `yaml:"skill"`
	Actions *yaml.Node `yaml:"actions"`
	Events  *struct {
		Protocol string `yaml:"protocol"`
		Command  string `yaml:"command"`
		Types    []struct {
			Type   string `yaml:"type"`
			Schema string `yaml:"schema"`
		} `yaml:"types"`
		Format   string     `yaml:"format"`
		Poll     string     `yaml:"poll"`
		ID       string     `yaml:"id"`
		TS       string     `yaml:"ts"`
		Identity string     `yaml:"identity"`
		Catchup  *yaml.Node `yaml:"catchup"`
	} `yaml:"events"`
	Channel *yaml.Node `yaml:"channel"`
}

// Install is the install block.
type Install struct {
	Catalog       bool           `yaml:"catalog"`
	GitHubRelease *GitHubRelease `yaml:"github_release"`
	Brew          string         `yaml:"brew"`
	Go            string         `yaml:"go"`
	Bin           string         `yaml:"bin"`
}

// GitHubRelease is the github_release install method.
type GitHubRelease struct {
	Repo      string `yaml:"repo"`
	Tag       string `yaml:"tag"`
	Asset     string `yaml:"asset"`
	Checksums string `yaml:"checksums"`
}

// Capabilities lists the blocks the manifest has.
func (m Manifest) Capabilities() []string {
	out := []string{}
	if m.Actions != nil {
		out = append(out, "actions")
	}
	if m.Events != nil {
		out = append(out, "events")
	}
	if m.Channel != nil {
		out = append(out, "channel")
	}
	return out
}

// Protocol returns the events protocol, or "" with no events block.
func (m Manifest) Protocol() string {
	switch {
	case m.Events == nil:
		return ""
	case m.Events.Protocol == "":
		return "lines"
	}
	return m.Events.Protocol
}

// CatalogBin returns the program that the catalog builds and hosts, or ""
// when the entry has no install.catalog.
func (m Manifest) CatalogBin() string {
	if m.Install == nil || !m.Install.Catalog {
		return ""
	}
	return m.Install.Bin
}

// Targets returns the platforms of the entry.
func (m Manifest) Targets() []string {
	if len(m.Platforms) > 0 {
		return m.Platforms
	}
	return DefaultPlatforms
}

// SkillDir is the skill folder relative to the entry folder.
func (m Manifest) SkillDir() string {
	if m.Skill != nil && m.Skill.Path != "" {
		return m.Skill.Path
	}
	return "skill"
}

// Entry is one catalog folder.
type Entry struct {
	Dir      string
	Manifest Manifest
	doc      any // the JSON form, for the schema
}

// LoadEntry reads the manifest of the folder dir.
func LoadEntry(dir string) (*Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	e := &Entry{Dir: dir}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if err := json.Unmarshal(b, &e.doc); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &e.Manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return e, nil
}

// SourceDir is connectors/<bin> of the repository, where the source of a
// catalog-built program lives.
func (e *Entry) SourceDir() string {
	return filepath.Join(e.Dir, "..", "..", "connectors", e.Manifest.CatalogBin())
}

// Name is the folder name, which must equal the manifest name.
func (e *Entry) Name() string { return filepath.Base(e.Dir) }

// Validate checks the entry against the schema and the rules the schema
// cannot express. It returns every problem it finds.
func (e *Entry) Validate(schema *jsonschema.Resolved) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	m := e.Manifest
	if err := schema.Validate(e.doc); err != nil {
		add("schema: %v", err)
	}
	if m.Name != e.Name() {
		add("name %q must equal the folder name %q", m.Name, e.Name())
	}
	if !semverRE.MatchString(m.Version) {
		add("version %q is not semver", m.Version)
	}
	if m.Icon != "" {
		e.checkFile(add, "icon", m.Icon)
	}
	if in := m.Install; in != nil {
		if gh := in.GitHubRelease; gh != nil {
			for _, s := range []string{gh.Tag, gh.Asset} {
				for _, k := range templateRE.FindAllString(s, -1) {
					if k != "{version}" && k != "{os}" && k != "{arch}" {
						add("install.github_release: unknown template %s", k)
					}
				}
			}
		}
		if (in.Catalog || in.GitHubRelease != nil || in.Brew != "" || in.Go != "") && in.Bin == "" {
			add("install.bin is required with an install method")
		}
		if in.Catalog && in.Bin != "" {
			if st, err := os.Stat(filepath.Join(e.SourceDir(), "scripts", "build.sh")); err != nil || !st.Mode().IsRegular() {
				add("install.catalog: connectors/%s/scripts/build.sh is missing", in.Bin)
			}
		}
	}
	if m.Setup != nil && len(m.Setup.Login) > 0 && m.Protocol() != ProtocolConnector {
		add("setup.login needs events.protocol: %s", ProtocolConnector)
	}
	if m.Channel != nil && m.Protocol() != ProtocolConnector {
		add("channel needs events.protocol: %s", ProtocolConnector)
	}
	if ev := m.Events; ev != nil {
		if m.Protocol() == ProtocolConnector && (ev.Format != "" || ev.Poll != "" || ev.ID != "" || ev.TS != "" || ev.Identity != "" || ev.Catchup != nil) {
			add("events: format, poll, id, ts, identity, and catchup are only for protocol: lines")
		}
		for _, t := range ev.Types {
			if t.Schema == "" {
				continue
			}
			if e.checkFile(add, "events.types["+t.Type+"].schema", t.Schema) {
				e.checkJSONSchema(add, t.Schema)
			}
		}
	}
	e.checkSkill(add)
	e.checkTree(add)
	return errs
}

// checkFile reports whether rel is a regular file in the entry folder.
func (e *Entry) checkFile(add func(string, ...any), field, rel string) bool {
	if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
		add("%s: %q must be a path in the entry folder", field, rel)
		return false
	}
	st, err := os.Lstat(filepath.Join(e.Dir, rel))
	if err != nil || !st.Mode().IsRegular() {
		add("%s: %q is not a file in the entry folder", field, rel)
		return false
	}
	return true
}

func (e *Entry) checkJSONSchema(add func(string, ...any), rel string) {
	data, err := os.ReadFile(filepath.Join(e.Dir, rel))
	if err != nil {
		add("%s: %v", rel, err)
		return
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		add("%s: %v", rel, err)
		return
	}
	if _, err := s.Resolve(nil); err != nil {
		add("%s: %v", rel, err)
	}
}

// checkSkill checks the frontmatter of the skill: name and description.
func (e *Entry) checkSkill(add func(string, ...any)) {
	rel := filepath.Join(e.Manifest.SkillDir(), "SKILL.md")
	data, err := os.ReadFile(filepath.Join(e.Dir, rel))
	if err != nil {
		add("skill: %s is missing", rel)
		return
	}
	fm, err := Frontmatter(data)
	if err != nil {
		add("skill: %s: %v", rel, err)
		return
	}
	for _, k := range []string{"name", "description"} {
		if s, _ := fm[k].(string); strings.TrimSpace(s) == "" {
			add("skill: %s: frontmatter needs %s", rel, k)
		}
	}
}

// Frontmatter reads the YAML frontmatter of a Markdown file.
func Frontmatter(data []byte) (map[string]any, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, errors.New("no frontmatter")
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil, errors.New("frontmatter is not closed")
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(s[4:4+end]), &fm); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	return fm, nil
}

// checkTree allows only regular files and folders, and bounds the size.
func (e *Entry) checkTree(add func(string, ...any)) {
	var total int64
	err := filepath.WalkDir(e.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			add("%s: symbolic links are not allowed", p)
			return nil
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		add("%v", err)
	}
	if total > maxEntrySize {
		add("the entry is %d bytes; the limit is %d", total, maxEntrySize)
	}
}

// files lists the snapshot files, sorted. Dot files are left out.
func (e *Entry) files() ([]string, error) {
	var out []string
	err := filepath.WalkDir(e.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(e.Dir, p)
		if err != nil {
			return err
		}
		if rel != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// Tar writes the entry folder as a tar stream with sorted names, zero
// times, and owner 0. The files sit at the root of the stream.
func (e *Entry) Tar() ([]byte, error) {
	files, err := e.files()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, f := range files {
		for d := pathDir(f); d != ""; d = pathDir(d) {
			if dirs[d] {
				break
			}
			dirs[d] = true
		}
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for d := range dirs {
			if !yield(d + "/") {
				return
			}
		}
		for _, f := range files {
			if !yield(f) {
				return
			}
		}
	})
	for _, name := range names {
		hdr := &tar.Header{Name: name, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o755
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.Dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, 0o644, int64(len(data))
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Snapshot returns the gzip of Tar.
func (e *Entry) Snapshot() ([]byte, error) {
	t, err := e.Tar()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(t); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	return p[:i]
}

// LoadSchema resolves the manifest JSON Schema at path.
func LoadSchema(path string) (*jsonschema.Resolved, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s.Resolve(nil)
}
