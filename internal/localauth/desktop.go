// Package localauth reads Slack sessions that exist on this machine: the
// Slack desktop app (xoxc- token and "d" cookie) and gammons/slk token files.
package localauth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	_ "modernc.org/sqlite"
)

var (
	ErrDesktopNotFound = errors.New("slack desktop app data not found")
	ErrNotSignedIn     = errors.New("the slack desktop app has no signed-in workspace")
	ErrNoCookie        = errors.New("slack desktop cookie not found")
	ErrDecrypt         = errors.New("cannot decrypt the slack desktop cookie")
)

// Session is the credentials of one workspace.
type Session struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Domain   string `json:"domain"`
	URL      string `json:"url,omitempty"`
	Token    string `json:"access_token"`
	Cookie   string `json:"cookie"`
}

// Desktop is the Slack desktop app data dir.
type Desktop struct {
	Dir string
	// Passwords returns the "Slack Safe Storage" key candidates.
	Passwords func() ([][]byte, error)
}

// NewDesktop finds the Slack desktop app data dir. SLACK_DESKTOP_DIR overrides it.
func NewDesktop() (*Desktop, error) {
	dir := os.Getenv("SLACK_DESKTOP_DIR")
	if dir == "" {
		dir = desktopDir(runtime.GOOS, os.Getenv)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w (looked in %s)", ErrDesktopNotFound, dir)
	}
	return &Desktop{Dir: dir, Passwords: keyringPasswords}, nil
}

func desktopDir(goos string, getenv func(string) string) string {
	home := getenv("HOME")
	var candidates []string
	switch goos {
	case "darwin":
		candidates = []string{
			filepath.Join(home, "Library", "Application Support", "Slack"),
			filepath.Join(home, "Library", "Containers", "com.tinyspeck.slackmacgap", "Data", "Library", "Application Support", "Slack"),
		}
	case "windows":
		candidates = []string{
			filepath.Join(getenv("APPDATA"), "Slack"),
			filepath.Join(getenv("LOCALAPPDATA"), "Packages", "com.tinyspeck.slackdesktop_8yrtsj140pw4g", "LocalCache", "Roaming", "Slack"),
		}
	default:
		if x := getenv("XDG_CONFIG_HOME"); x != "" {
			candidates = append(candidates, filepath.Join(x, "Slack"))
		}
		candidates = append(candidates,
			filepath.Join(home, ".config", "Slack"),
			filepath.Join(home, ".var", "app", "com.slack.Slack", "config", "Slack"),
			filepath.Join(home, "snap", "slack", "current", ".config", "Slack"),
		)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return candidates[0]
}

// Sessions returns the signed-in workspaces with their token and cookie.
// A workspace without a token in Local Storage gets one from its web page
// (see Mint).
func (d *Desktop) Sessions(mint func(domain, cookie string) (string, error)) ([]Session, error) {
	teams, err := d.Workspaces()
	if err != nil {
		return nil, err
	}
	tokens, err := d.Tokens()
	if err != nil && !errors.Is(err, ErrNotSignedIn) {
		return nil, err
	}
	cookie, err := d.Cookie()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, t := range teams {
		t.Cookie = cookie
		t.Token = tokens[t.TeamID]
		if t.Token == "" && mint != nil && t.Domain != "" {
			if t.Token, err = mint(t.Domain, cookie); err != nil {
				return nil, fmt.Errorf("%s: %w", t.Domain, err)
			}
		}
		if t.Token != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, ErrNotSignedIn
	}
	return out, nil
}

// Workspaces reads the signed-in workspaces from storage/root-state.json.
func (d *Desktop) Workspaces() ([]Session, error) {
	data, err := os.ReadFile(filepath.Join(d.Dir, "storage", "root-state.json"))
	if err != nil {
		return nil, ErrNotSignedIn
	}
	return parseRootState(data)
}

func parseRootState(data []byte) ([]Session, error) {
	var rs struct {
		Workspaces map[string]struct {
			Name   string `json:"name"`
			URL    string `json:"url"`
			Domain string `json:"domain"`
			ID     string `json:"id"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	var out []Session
	for _, w := range rs.Workspaces {
		if w.ID == "" {
			continue
		}
		out = append(out, Session{TeamID: w.ID, TeamName: w.Name, Domain: w.Domain, URL: w.URL})
	}
	if len(out) == 0 {
		return nil, ErrNotSignedIn
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TeamName < out[j].TeamName })
	return out, nil
}

// Tokens reads team id → xoxc- token from Local Storage (key localConfig_v2).
func (d *Desktop) Tokens() (map[string]string, error) {
	src := filepath.Join(d.Dir, "Local Storage", "leveldb")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil, ErrNotSignedIn
	}
	// Slack holds a lock on the live DB, so read a copy.
	tmp, err := copyDir(src)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	return tokensFromLevelDB(tmp)
}

func tokensFromLevelDB(dir string) (map[string]string, error) {
	db, err := leveldb.OpenFile(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]string{}
	it := db.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		if !strings.HasSuffix(string(it.Key()), "localConfig_v2") {
			continue
		}
		var cfg struct {
			Teams map[string]struct {
				Token string `json:"token"`
			} `json:"teams"`
		}
		if json.Unmarshal([]byte(decodeLSValue(it.Value())), &cfg) != nil {
			continue
		}
		for id, t := range cfg.Teams {
			if t.Token != "" {
				out[id] = t.Token
			}
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotSignedIn
	}
	return out, nil
}

// decodeLSValue decodes a Chromium localStorage value: a 0x00 prefix is
// UTF-16LE, a 0x01 prefix is Latin-1.
func decodeLSValue(v []byte) string {
	if len(v) == 0 {
		return ""
	}
	switch v[0] {
	case 0x00:
		b := v[1:]
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(u))
	case 0x01:
		return string(v[1:])
	}
	return string(v)
}

// Cookie reads and decrypts the "d" cookie of .slack.com.
func (d *Desktop) Cookie() (string, error) {
	db := filepath.Join(d.Dir, "Cookies")
	if runtime.GOOS == "windows" {
		db = filepath.Join(d.Dir, "Network", "Cookies")
	}
	plain, enc, err := readCookie(db)
	if err != nil {
		return "", err
	}
	if plain != "" {
		return plain, nil
	}
	v, err := decryptCookie(runtime.GOOS, enc, d.Passwords)
	if err != nil {
		return "", err
	}
	return string(v), nil
}

func readCookie(path string) (string, []byte, error) {
	if _, err := os.Stat(path); err != nil {
		return "", nil, ErrNoCookie
	}
	tmp, err := os.CreateTemp("", "slack-cli-cookies-*.db")
	if err != nil {
		return "", nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := copyFile(path, tmp.Name()); err != nil {
		return "", nil, err
	}
	db, err := sql.Open("sqlite", tmp.Name())
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	var plain string
	var enc []byte
	err = db.QueryRow(`SELECT value, encrypted_value FROM cookies WHERE host_key='.slack.com' AND name='d'`).Scan(&plain, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNoCookie
	}
	return plain, enc, err
}

func copyDir(src string) (string, error) {
	dst, err := os.MkdirTemp("", "slack-cli-ls-*")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		os.RemoveAll(dst)
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "LOCK" {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			os.RemoveAll(dst)
			return "", err
		}
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
