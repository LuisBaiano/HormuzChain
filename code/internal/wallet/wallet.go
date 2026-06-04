package wallet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

// GenerateKeyPair generates a new ECDSA private key and public key as hex strings.
func GenerateKeyPair() (string, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	privBytes := priv.D.Bytes()
	privHex := hex.EncodeToString(privBytes)

	pubBytes := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	pubHex := hex.EncodeToString(pubBytes)

	return privHex, pubHex, nil
}

// GetAddress derives the company/vessel account address from the public key hex using SHA-256.
func GetAddress(pubHex string) string {
	hasher := sha256.New()
	hasher.Write([]byte(pubHex))
	return "0x" + hex.EncodeToString(hasher.Sum(nil))[:20]
}

// Sign signs a message hash using the hex private key.
func Sign(privHex string, msg []byte) (string, error) {
	privBytes, err := hex.DecodeString(privHex)
	if err != nil {
		return "", err
	}
	d := new(big.Int).SetBytes(privBytes)
	priv := &ecdsa.PrivateKey{
		D: d,
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
		},
	}
	priv.PublicKey.X, priv.PublicKey.Y = elliptic.P256().ScalarBaseMult(privBytes)

	hash := sha256.Sum256(msg)
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s,%s", r.Text(16), s.Text(16)), nil
}

// Verify verifies a signature against a public key hex and message byte slice.
func Verify(pubHex string, msg []byte, sigStr string) bool {
	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		return false
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pubBytes)
	if x == nil || y == nil {
		return false
	}
	pub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     x,
		Y:     y,
	}

	parts := splitComma(sigStr)
	if len(parts) != 2 {
		return false
	}
	rStr, sStr := parts[0], parts[1]

	r, ok := new(big.Int).SetString(rStr, 16)
	if !ok {
		return false
	}
	s, ok := new(big.Int).SetString(sStr, 16)
	if !ok {
		return false
	}

	hash := sha256.Sum256(msg)
	return ecdsa.Verify(pub, hash[:], r, s)
}

func splitComma(s string) []string {
	var res []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			res = append(res, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		res = append(res, cur)
	}
	return res
}
