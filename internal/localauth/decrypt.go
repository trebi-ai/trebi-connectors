package localauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"fmt"
)

// slackHashPrefixes are the SHA-256 domain hashes that Chromium 130+ puts in
// front of a decrypted cookie value (slack.com and .slack.com).
var slackHashPrefixes = [][]byte{
	{3, 202, 236, 172, 132, 247, 212, 240, 217, 211, 68, 226, 103, 153, 245, 64, 85, 68, 2, 183, 83, 182, 186, 218, 14, 102, 237, 62, 231, 241, 231, 142},
	{145, 28, 115, 68, 173, 92, 42, 78, 104, 243, 5, 63, 24, 206, 51, 190, 31, 169, 160, 244, 247, 106, 147, 228, 60, 68, 92, 134, 105, 199, 162, 120},
}

// decryptCookie decrypts a Chromium cookie value ("v10"/"v11" prefix). It
// tries each key candidate until one gives printable text: the PKCS#7 check
// alone lets a wrong key through about one time in sixteen.
func decryptCookie(goos string, enc []byte, passwords func() ([][]byte, error)) ([]byte, error) {
	if len(enc) < 3 {
		return nil, ErrDecrypt
	}
	version, body := string(enc[:3]), enc[3:]
	if goos == "linux" && version == "v10" {
		return printable(decryptCBC(body, []byte("peanuts"), 1))
	}
	if goos == "windows" {
		return nil, fmt.Errorf("%w: Windows is not supported, use `slack-cli auth add --token xoxc-... --cookie xoxd-...`", ErrDecrypt)
	}
	pws, err := passwords()
	if err != nil {
		return nil, err
	}
	rounds := 1
	if goos == "darwin" {
		rounds = 1003
	}
	lastErr := ErrDecrypt
	for _, pw := range pws {
		out, err := printable(decryptCBC(body, pw, rounds))
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// decryptCBC is the Chromium AES-128-CBC scheme: PBKDF2-SHA1 key with salt
// "saltysalt" and an IV of 16 spaces.
func decryptCBC(body, password []byte, rounds int) ([]byte, error) {
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: bad length %d", ErrDecrypt, len(body))
	}
	key, err := pbkdf2.Key(sha1.New, string(password), []byte("saltysalt"), rounds, 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, 16)).CryptBlocks(out, body)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize {
		return nil, fmt.Errorf("%w: bad padding", ErrDecrypt)
	}
	out = out[:len(out)-n]
	for _, p := range slackHashPrefixes {
		if bytes.HasPrefix(out, p) {
			return out[len(p):], nil
		}
	}
	return out, nil
}

func printable(b []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, ErrDecrypt
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return nil, fmt.Errorf("%w: wrong Safe Storage key", ErrDecrypt)
		}
	}
	return b, nil
}
