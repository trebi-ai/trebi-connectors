package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const schemaPath = "../../schema/trebi-connector.schema.json"

const goodManifest = `schema: trebi-connector/1
name: demo
title: Demo
description: A demo.
version: 1.0.0
setup:
  inputs:
    - {name: DEMO_KEY, label: API key, secret: true, required: true}
actions:
  cli: {commands: [demo-cli]}
`

func writeEntry(t *testing.T, root, name, manifest, skill string) string {
	t.Helper()
	dir := filepath.Join(root, "catalog", name)
	files := map[string]string{ManifestFile: manifest, "skill/SKILL.md": skill}
	for p, c := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const goodSkill = "---\nname: demo-cli\ndescription: Use demo-cli.\n---\nBody.\n"

func TestRepositoryCatalog(t *testing.T) {
	entries, err := loadEntries("../..", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	var out bytes.Buffer
	if err := validate(context.Background(), entries, schemaPath, false, ReleaseChecker{}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestValidate(t *testing.T) {
	schema, err := LoadSchema(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, folder, manifest, skill, want string
	}{
		{"good", "demo", goodManifest, goodSkill, ""},
		{"folder", "other", goodManifest, goodSkill, "folder name"},
		{"unknown key", "demo", goodManifest + "extra: 1\n", goodSkill, "schema"},
		{"no description", "demo", goodManifest, "---\nname: demo-cli\n---\n", "frontmatter needs description"},
		{"no frontmatter", "demo", goodManifest, "# Demo\n", "no frontmatter"},
		{"lines events", "demo", goodManifest + "events: {command: demo-cli listen}\n", goodSkill, ""},
		{"bad version", "demo", strings.Replace(goodManifest, "1.0.0", "1.0", 1), goodSkill, "semver"},
		{"missing schema file", "demo", goodManifest + "events:\n  protocol: trebi-connector/1\n  command: demo serve\n  types: [{type: message, schema: schemas/m.json}]\n", goodSkill, "not a file"},
		{"channel on lines", "demo", goodManifest + "events: {command: demo-cli listen}\nchannel: {features: [typing]}\n", goodSkill, "channel needs events.protocol"},
		{"install without bin", "demo", goodManifest + "install: {go: \"example.com/demo@v{version}\"}\n", goodSkill, "install.bin"},
		{"bad template", "demo", goodManifest + "install:\n  github_release: {repo: a/b, tag: \"v{ver}\", asset: x}\n  bin: demo\n", goodSkill, "unknown template {ver}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeEntry(t, t.TempDir(), tc.folder, tc.manifest, tc.skill)
			e, err := LoadEntry(dir)
			if err != nil {
				t.Fatal(err)
			}
			errs := errors.Join(e.Validate(schema)...)
			switch {
			case tc.want == "" && errs != nil:
				t.Fatalf("want no error, got %v", errs)
			case tc.want != "" && (errs == nil || !strings.Contains(errs.Error(), tc.want)):
				t.Fatalf("want %q, got %v", tc.want, errs)
			}
		})
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	dir := writeEntry(t, t.TempDir(), "demo", goodManifest, goodSkill)
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := LoadEntry(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, ManifestFile), now, now); err != nil {
		t.Fatal(err)
	}
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two snapshots of the same files differ")
	}
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" {
			t.Fatalf("header %s: %+v", h.Name, h)
		}
		names = append(names, h.Name)
	}
	if got := strings.Join(names, ","); got != "skill/,skill/SKILL.md,trebi-connector.yaml" {
		t.Fatalf("names: %s", got)
	}
}

type mapFetch map[string][]byte

func (m mapFetch) Get(_ context.Context, url string) ([]byte, error) {
	if b, ok := m[url]; ok {
		return b, nil
	}
	return nil, errors.New("404 " + url)
}

func TestBuild(t *testing.T) {
	root := t.TempDir()
	demo, err := LoadEntry(writeEntry(t, root, "demo", goodManifest, goodSkill))
	if err != nil {
		t.Fatal(err)
	}
	held, err := LoadEntry(writeEntry(t, root, "held", strings.Replace(goodManifest, "name: demo", "name: held", 1), goodSkill))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	b := &Builder{BaseURL: BaseURL, Now: now, Log: io.Discard}
	o, err := b.Build(context.Background(), []*Entry{demo, held}, map[string]string{"held": "no release"})
	if err != nil {
		t.Fatal(err)
	}
	var idx Index
	if err := json.Unmarshal(o.Index, &idx); err != nil {
		t.Fatal(err)
	}
	snap := o.Snapshots["snapshots/demo/1.0.0.tar.gz"]
	if len(idx.Entries) != 1 || len(o.Snapshots) != 1 || snap == nil {
		t.Fatalf("index %s", o.Index)
	}
	ie := idx.Entries[0]
	if ie.Name != "demo" || ie.Latest != "1.0.0" || ie.Publisher != "community" || ie.Capabilities[0] != "actions" ||
		ie.Versions[0].URL != "https://catalog.trebi.ai/v1/snapshots/demo/1.0.0.tar.gz" ||
		ie.Versions[0].Digest != Digest(snap) || ie.Versions[0].PublishedAt != "2026-09-28T12:00:00Z" {
		t.Fatalf("entry %+v", ie)
	}

	// The same version again keeps its record and uploads nothing.
	b2 := &Builder{BaseURL: BaseURL, Previous: &idx, Now: now.Add(time.Hour), Log: io.Discard}
	o2, err := b2.Build(context.Background(), []*Entry{demo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(o2.Snapshots) != 0 || !strings.Contains(string(o2.Index), "2026-09-28T12:00:00Z") {
		t.Fatalf("rebuild: %d snapshots\n%s", len(o2.Snapshots), o2.Index)
	}

	// Other gzip bytes of the same tar are the same content.
	var regz bytes.Buffer
	zw := gzip.NewWriter(&regz)
	tarBytes, err := demo.Tar()
	if err != nil {
		t.Fatal(err)
	}
	zw.Write(tarBytes)
	zw.Close()
	prev := idx
	prev.Entries = []IndexEntry{ie}
	prev.Entries[0].Versions = []IndexVersion{{Version: "1.0.0", Digest: Digest(regz.Bytes()), URL: ie.Versions[0].URL, PublishedAt: "x"}}
	b3 := &Builder{BaseURL: BaseURL, Previous: &prev, Fetch: mapFetch{ie.Versions[0].URL: regz.Bytes()}, Now: now, Log: io.Discard}
	if _, err := b3.Build(context.Background(), []*Entry{demo}, nil); err != nil {
		t.Fatalf("same content: %v", err)
	}

	// Changed content with the same version fails.
	if err := os.WriteFile(filepath.Join(demo.Dir, "skill", "SKILL.md"), []byte(goodSkill+"More.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b4 := &Builder{BaseURL: BaseURL, Previous: &idx, Fetch: mapFetch{ie.Versions[0].URL: snap}, Now: now, Log: io.Discard}
	if _, err := b4.Build(context.Background(), []*Entry{demo}, nil); err == nil || !strings.Contains(err.Error(), "bump the version") {
		t.Fatalf("changed content: %v", err)
	}
}

func TestLatest(t *testing.T) {
	vs := []IndexVersion{{Version: "1.0.0"}, {Version: "1.2.0-rc.1"}, {Version: "1.1.0", Yanked: true}, {Version: "1.0.0+2"}}
	if got := latest(sorted(vs)); got != "1.0.0+2" {
		t.Fatalf("latest %s", got)
	}
	if got := latest([]IndexVersion{{Version: "0.1.0-rc.1"}}); got != "0.1.0-rc.1" {
		t.Fatalf("only pre-release: %s", got)
	}
	if CompareSemver("1.10.0", "1.9.0") != 1 || CompareSemver("1.0.0-rc.2", "1.0.0-rc.10") != -1 || CompareSemver("1.0.0-rc.1", "1.0.0") != -1 {
		t.Fatal("compare")
	}
}

func sorted(vs []IndexVersion) []IndexVersion {
	out := append([]IndexVersion(nil), vs...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if CompareSemver(out[j].Version, out[i].Version) < 0 {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestSigning(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	key, err := ParseSigningKey(base64.StdEncoding.EncodeToString(seed), pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSigningKey(base64.StdEncoding.EncodeToString(seed), PublicKey); err == nil {
		t.Fatal("a throwaway key must not match the catalog key")
	}
	if _, err := ParseSigningKey("", ""); err == nil {
		t.Fatal("an empty key must fail")
	}
	dir := t.TempDir()
	o := &Output{Index: []byte(`{"entries":[]}` + "\n"), Snapshots: map[string][]byte{"snapshots/a/1.0.0.tar.gz": []byte("x")}}
	if err := o.Write(dir, key); err != nil {
		t.Fatal(err)
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "v1", "index.json"))
	sig, _ := os.ReadFile(filepath.Join(dir, "v1", "index.json.sig"))
	raw, err := base64.StdEncoding.DecodeString(string(sig))
	if err != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), idx, raw) {
		t.Fatalf("signature does not verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "v1", "snapshots", "a", "1.0.0.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseChecker(t *testing.T) {
	assets := map[string][]byte{}
	var sums strings.Builder
	for _, p := range DefaultPlatforms {
		name := "demo-cli_1.0.0_" + strings.Replace(p, "/", "_", 1) + ".tar.gz"
		assets[name] = []byte("bin " + p)
		sum := sha256.Sum256(assets[name])
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	assets["SHA256SUMS"] = []byte(sums.String())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/acme/demo/releases/download/channels/demo-cli/v1.0.0/")
		b, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	var m Manifest
	m.Version = "1.0.0"
	m.Install = &struct {
		GitHubRelease *GitHubRelease `yaml:"github_release"`
		Brew          string         `yaml:"brew"`
		Go            string         `yaml:"go"`
		Bin           string         `yaml:"bin"`
	}{GitHubRelease: &GitHubRelease{Repo: "acme/demo", Tag: "channels/demo-cli/v{version}", Asset: "demo-cli_{version}_{os}_{arch}.tar.gz", Checksums: "SHA256SUMS"}, Bin: "demo-cli"}
	rc := ReleaseChecker{Fetch: HTTP{Client: srv.Client()}, GitHubURL: srv.URL}
	if err := rc.Check(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	assets["demo-cli_1.0.0_linux_arm64.tar.gz"] = []byte("tampered")
	if err := rc.Check(context.Background(), m); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered: %v", err)
	}
	delete(assets, "demo-cli_1.0.0_linux_arm64.tar.gz")
	if err := rc.Check(context.Background(), m); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing: %v", err)
	}
}
