// Package enc holds the runner's end-to-end encryption helpers. Task content is
// sealed to public keys with NaCl sealed boxes (X25519 + XSalsa20-Poly1305):
// the browser seals prompts to the runner's public key, and the runner seals
// log output to the user's public key. Keys are 32-byte X25519 keys, exchanged
// base64-encoded. See docs/BRIEFING.md section 7.
package enc

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/nacl/box"
)

// Keypair is a base64-encoded X25519 keypair.
type Keypair struct {
	PublicKey  string
	PrivateKey string
}

// GenerateKeypair creates a fresh X25519 keypair for sealed-box encryption.
func GenerateKeypair() (Keypair, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return Keypair{}, fmt.Errorf("generate x25519 keypair: %w", err)
	}

	return Keypair{
		PublicKey:  base64.StdEncoding.EncodeToString(pub[:]),
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
	}, nil
}
