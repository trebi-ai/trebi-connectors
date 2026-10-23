package vnc

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"testing"
)

// ardServer runs the server half of ARD over ardResponse and returns the
// 128 bytes that the server decrypts (vncdotool tests/unit/test_security.py).
func ardServer(t *testing.T, creds []byte) []byte {
	t.Helper()
	p, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DD"+
		"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"+
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F"+
		"83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B"+
		"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA0510"+
		"15728E5A8AACAA68FFFFFFFFFFFFFFFF", 16)
	const keyLen = 256
	x, _ := rand.Int(rand.Reader, p)
	pub := new(big.Int).Exp(big.NewInt(2), x, p)
	sent, err := ardResponse(2, p.FillBytes(make([]byte, keyLen)), pub.FillBytes(make([]byte, keyLen)), creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 128+keyLen {
		t.Fatalf("sent %d bytes", len(sent))
	}
	shared := new(big.Int).Exp(new(big.Int).SetBytes(sent[128:]), x, p).FillBytes(make([]byte, keyLen))
	digest := md5.Sum(shared)
	block, _ := aes.NewCipher(digest[:])
	plain := make([]byte, 128)
	for i := 0; i < 128; i += aes.BlockSize {
		block.Decrypt(plain[i:], sent[i:])
	}
	return plain
}

func TestARDCredentialsFit(t *testing.T) {
	for _, tc := range []struct{ name, user, password string }{
		{"ascii", "alice", "secret"},
		{"non ascii", "üser", "pässwörd"},
		{"outside the bmp", "al🙂ce", "s🔑cret"},
		{"longest ascii", strings.Repeat("a", 63), strings.Repeat("b", 63)},
		{"non ascii at the limit", strings.Repeat("é", 31) + "z", strings.Repeat("ü", 31) + "z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := ardCredentials(tc.user, tc.password)
			if err != nil {
				t.Fatal(err)
			}
			plain := ardServer(t, creds)
			for _, f := range []struct {
				value string
				field []byte
			}{{tc.user, plain[:64]}, {tc.password, plain[64:]}} {
				if !bytes.HasPrefix(f.field, []byte(f.value)) || f.field[len(f.value)] != 0 {
					t.Fatalf("field %q does not hold %q and a NUL", f.field, f.value)
				}
			}
		})
	}
}

func TestARDPaddingIsRandom(t *testing.T) {
	a, _ := ardCredentials("alice", "secret")
	b, _ := ardCredentials("alice", "secret")
	if bytes.Equal(a[6:64], b[6:64]) || bytes.Equal(a[71:128], b[71:128]) {
		t.Fatal("the padding is the same twice")
	}
}

func TestARDCredentialsDoNotFit(t *testing.T) {
	for _, tc := range []struct{ name, user, password, field string }{
		{"user with no room for the NUL", strings.Repeat("a", 64), "secret", "user"},
		{"long password", "alice", strings.Repeat("b", 200), "password"},
		{"non ascii over 64 bytes", strings.Repeat("é", 40), "secret", "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ardCredentials(tc.user, tc.password)
			var ae *AuthError
			if !errors.As(err, &ae) || !strings.Contains(ae.Reason, "the "+tc.field+" is") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), tc.password) {
				t.Fatal("error holds the password")
			}
		})
	}
}

func TestARDBadServerKey(t *testing.T) {
	p := big.NewInt(23).FillBytes(make([]byte, 2))
	for _, y := range []int64{0, 1, 22, 23} {
		if _, err := ardResponse(5, p, big.NewInt(y).FillBytes(make([]byte, 2)), make([]byte, 128)); err == nil {
			t.Errorf("server key %d gave no error", y)
		}
	}
}
