package localauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"testing"
)

// encryptCBC mirrors decryptCBC for the test: it produces a Chromium-style
// "v10" cookie blob for a known Safe Storage password.
func encryptCBC(t *testing.T, plain, password []byte, rounds int, prefix []byte) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, string(password), []byte("saltysalt"), rounds, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	msg := append(append([]byte{}, prefix...), plain...)
	pad := aes.BlockSize - len(msg)%aes.BlockSize
	msg = append(msg, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(msg))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, 16)).CryptBlocks(out, msg)
	return append([]byte("v10"), out...)
}

func TestDecryptCookieDarwinRoundTrip(t *testing.T) {
	pw := []byte("test-safe-storage-key")
	want := []byte("xoxd-abc123def456")
	enc := encryptCBC(t, want, pw, 1003, nil)
	got, err := decryptCookie("darwin", enc, func() ([][]byte, error) {
		return [][]byte{[]byte("wrong-key-000000"), pw}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecryptCookieStripsDomainHashPrefix(t *testing.T) {
	pw := []byte("key-with-prefix")
	want := []byte("xoxd-withprefix")
	enc := encryptCBC(t, want, pw, 1003, slackHashPrefixes[0])
	got, err := decryptCookie("darwin", enc, func() ([][]byte, error) {
		return [][]byte{pw}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("prefix not stripped: got %q, want %q", got, want)
	}
}

func TestDecryptCookieLinux(t *testing.T) {
	want := []byte("xoxd-linuxcookie")
	enc := encryptCBC(t, want, []byte("peanuts"), 1, nil)
	got, err := decryptCookie("linux", enc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecryptCookieWrongKeyFails(t *testing.T) {
	enc := encryptCBC(t, []byte("xoxd-secret"), []byte("real-key"), 1003, nil)
	_, err := decryptCookie("darwin", enc, func() ([][]byte, error) {
		return [][]byte{[]byte("bad-key")}, nil
	})
	if err == nil {
		t.Error("wrong key should fail")
	}
}

func TestDecryptCookieWindowsUnsupported(t *testing.T) {
	_, err := decryptCookie("windows", []byte("v10xxxxxxxxxxxxxx"), nil)
	if err == nil {
		t.Error("windows should be unsupported")
	}
}
