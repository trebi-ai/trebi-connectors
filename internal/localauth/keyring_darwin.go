package localauth

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

const safeStorage = "Slack Safe Storage"

// keyringPasswords reads each "Slack Safe Storage" item of the login
// keychain. There can be one item per Slack build (App Store and direct).
// macOS can ask once to let /usr/bin/security read the item.
func keyringPasswords() ([][]byte, error) {
	var out [][]byte
	seen := map[string]bool{}
	for _, acct := range append(keychainAccounts(), "") {
		pw, err := keychainPassword(acct)
		if err != nil || pw == "" || seen[pw] {
			continue
		}
		seen[pw] = true
		out = append(out, []byte(pw))
	}
	if len(out) == 0 {
		return nil, errors.New(`keychain item "Slack Safe Storage" not found or access denied`)
	}
	return out, nil
}

func keychainPassword(account string) (string, error) {
	args := []string{"find-generic-password", "-w", "-s", safeStorage}
	if account != "" {
		args = append(args, "-a", account)
	}
	var out bytes.Buffer
	cmd := exec.Command("/usr/bin/security", args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}

// keychainAccounts lists the accounts of the Safe Storage items.
// dump-keychain prints attributes only, not secrets.
func keychainAccounts() []string {
	var out bytes.Buffer
	cmd := exec.Command("/usr/bin/security", "dump-keychain")
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return nil
	}
	return parseKeychainAccounts(out.String())
}

func parseKeychainAccounts(dump string) []string {
	var out []string
	acct := ""
	for _, line := range strings.Split(dump, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "keychain:"):
			acct = ""
		case strings.HasPrefix(t, `"acct"`):
			acct = attrValue(t)
		case strings.HasPrefix(t, `"svce"`):
			if acct != "" && attrValue(t) == safeStorage {
				out = append(out, acct)
			}
		}
	}
	return out
}

func attrValue(line string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return ""
	}
	v := line[eq+1:]
	i, j := strings.Index(v, `"`), strings.LastIndex(v, `"`)
	if i < 0 || j <= i {
		return ""
	}
	return v[i+1 : j]
}
