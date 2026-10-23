package vnc

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
)

// ardFieldLen is the size of the user field and of the password field.
const ardFieldLen = 64

// maxARDKeyLen bounds the Diffie-Hellman key size that the client accepts
// (8192 bits).
const maxARDKeyLen = 1024

// secARD is Apple Remote Desktop (type 30): Diffie-Hellman, then the user
// and the password in AES-128-ECB under the MD5 of the shared key.
type secARD struct{}

func (secARD) handshake(h *handshake) error {
	hdr, err := h.read(4)
	if err != nil {
		return err
	}
	g, keyLen := binary.BigEndian.Uint16(hdr), int(binary.BigEndian.Uint16(hdr[2:]))
	if keyLen == 0 || keyLen > maxARDKeyLen {
		return protoErr("Apple Remote Desktop", "key length %d is not supported", keyLen)
	}
	mod, err := h.read(keyLen)
	if err != nil {
		return err
	}
	pub, err := h.read(keyLen)
	if err != nil {
		return err
	}
	if h.opts.User == "" || h.opts.Password == "" {
		return &AuthError{Reason: "the computer asks for a user and a password, and they are not both set"}
	}
	creds, err := ardCredentials(h.opts.User, h.opts.Password)
	if err != nil {
		return err
	}
	msg, err := ardResponse(g, mod, pub, creds)
	if err != nil {
		return err
	}
	if err := h.write(msg); err != nil {
		return err
	}
	return h.result()
}

// ardCredentials is the 128-byte block: each field is NUL-terminated UTF-8
// with random padding, as rfbproto says.
func ardCredentials(user, password string) ([]byte, error) {
	out := make([]byte, 0, 2*ardFieldLen)
	for _, f := range []struct{ name, value string }{{"user", user}, {"password", password}} {
		if len(f.value) >= ardFieldLen {
			return nil, &AuthError{Reason: fmt.Sprintf("the %s is %d bytes as UTF-8; Apple Remote Desktop carries at most %d", f.name, len(f.value), ardFieldLen-1)}
		}
		field := make([]byte, ardFieldLen)
		copy(field, f.value)
		rand.Read(field[len(f.value)+1:])
		out = append(out, field...)
	}
	return out, nil
}

// ardResponse is the ciphertext of creds and the public key of the client.
func ardResponse(g uint16, modulus, serverPub, creds []byte) ([]byte, error) {
	p := new(big.Int).SetBytes(modulus)
	y := new(big.Int).SetBytes(serverPub)
	one := big.NewInt(1)
	pm1 := new(big.Int).Sub(p, one)
	if p.Cmp(big.NewInt(3)) < 0 || y.Cmp(one) <= 0 || y.Cmp(pm1) >= 0 {
		return nil, protoErr("Apple Remote Desktop", "the server key is not valid")
	}
	// The private key is in [2, p-2].
	x, err := rand.Int(rand.Reader, new(big.Int).Sub(p, big.NewInt(3)))
	if err != nil {
		return nil, err
	}
	x.Add(x, big.NewInt(2))
	keyLen := len(modulus)
	shared := new(big.Int).Exp(y, x, p).FillBytes(make([]byte, keyLen))
	digest := md5.Sum(shared)
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(creds), len(creds)+keyLen)
	for i := 0; i < len(creds); i += aes.BlockSize {
		block.Encrypt(out[i:], creds[i:])
	}
	clientPub := new(big.Int).Exp(big.NewInt(int64(g)), x, p)
	return append(out, clientPub.FillBytes(make([]byte, keyLen))...), nil
}
