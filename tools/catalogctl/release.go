package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultPlatforms are the platforms of an entry with no platforms field.
var DefaultPlatforms = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}

// HTTP is a Fetcher over net/http.
type HTTP struct{ Client *http.Client }

func (h HTTP) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ReleaseChecker checks that the github_release assets of an entry exist
// and match their checksums.
type ReleaseChecker struct {
	Fetch     Fetcher
	GitHubURL string // https://github.com
}

// Check returns nil when the entry has no github_release, or when every
// platform asset is listed in the checksums file and matches it.
func (r ReleaseChecker) Check(ctx context.Context, m Manifest) error {
	if m.Install == nil || m.Install.GitHubRelease == nil {
		return nil
	}
	gh := m.Install.GitHubRelease
	platforms := m.Platforms
	if len(platforms) == 0 {
		platforms = DefaultPlatforms
	}
	base := strings.TrimSuffix(r.GitHubURL, "/") + "/" + gh.Repo + "/releases/download/"
	var sums map[string]string
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		tag := expand(gh.Tag, m.Version, goos, goarch)
		asset := expand(gh.Asset, m.Version, goos, goarch)
		if gh.Checksums != "" && sums == nil {
			data, err := r.Fetch.Get(ctx, base+tag+"/"+expand(gh.Checksums, m.Version, goos, goarch))
			if err != nil {
				return fmt.Errorf("checksums: %w", err)
			}
			sums = parseSums(data)
		}
		data, err := r.Fetch.Get(ctx, base+tag+"/"+asset)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if gh.Checksums == "" {
			continue
		}
		want, ok := sums[asset]
		if !ok {
			return fmt.Errorf("%s: %s is not in %s", p, asset, gh.Checksums)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("%s: %s does not match its checksum", p, asset)
		}
	}
	return nil
}

func expand(s, version, goos, goarch string) string {
	return strings.NewReplacer("{version}", version, "{os}", goos, "{arch}", goarch).Replace(s)
}

// parseSums reads "<hex>  <name>" lines.
func parseSums(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out
}
