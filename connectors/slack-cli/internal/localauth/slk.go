package localauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SlkDir returns the token dir of gammons/slk: $XDG_DATA_HOME/slk/tokens or
// ~/.local/share/slk/tokens.
func SlkDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "slk", "tokens")
}

// SlkSessions reads the gammons/slk token files (<team id>.json) in dir.
// A missing dir gives no sessions and no error.
func SlkSessions(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(data, &s) != nil || s.Token == "" {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TeamName < out[j].TeamName })
	return out, nil
}
