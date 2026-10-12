//go:build !darwin

package localauth

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// keyringPasswords reads the Safe Storage key from the Secret Service with
// secret-tool (libsecret).
func keyringPasswords() ([][]byte, error) {
	for _, schema := range []string{"chrome_libsecret_os_crypt_password_v2", "chrome_libsecret_os_crypt_password_v1"} {
		var out bytes.Buffer
		cmd := exec.Command("secret-tool", "lookup", "xdg:schema", schema, "application", "Slack")
		cmd.Stdout = &out
		if cmd.Run() == nil {
			if pw := strings.TrimRight(out.String(), "\r\n"); pw != "" {
				return [][]byte{[]byte(pw)}, nil
			}
		}
	}
	return nil, errors.New("slack Safe Storage key not found (needs secret-tool and an unlocked keyring)")
}
