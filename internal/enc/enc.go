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

	"golang.org/x/crypto/blake2b"
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

// OpenSealed decrypts a libsodium sealed box (crypto_box_seal): an anonymous
// message the browser sealed to the runner's X25519 public key. The layout is
// ephemeral_pubkey (32 bytes) || box, with the nonce derived as
// blake2b(ephemeral_pubkey || recipient_pubkey). publicKey and privateKey are
// the runner's raw 32-byte X25519 keys.
func OpenSealed(sealed, publicKey, privateKey []byte) ([]byte, error) {
	if 32 != len(publicKey) || 32 != len(privateKey) {
		return nil, fmt.Errorf("x25519 keys must be 32 bytes, got pub=%d priv=%d", len(publicKey), len(privateKey))
	}
	if len(sealed) < 32+box.Overhead {
		return nil, fmt.Errorf("sealed box too short: %d bytes", len(sealed))
	}

	var ephemeralPub, recipientPub, recipientPriv [32]byte
	copy(ephemeralPub[:], sealed[:32])
	copy(recipientPub[:], publicKey)
	copy(recipientPriv[:], privateKey)

	hash, err := blake2b.New(24, nil)
	if err != nil {
		return nil, fmt.Errorf("init blake2b: %w", err)
	}
	hash.Write(ephemeralPub[:])
	hash.Write(recipientPub[:])
	var nonce [24]byte
	copy(nonce[:], hash.Sum(nil))

	plaintext, ok := box.Open(nil, sealed[32:], &nonce, &ephemeralPub, &recipientPriv)
	if !ok {
		return nil, fmt.Errorf("sealed box authentication failed")
	}

	return plaintext, nil
}

// OpenSealedBase64 is OpenSealed over base64-encoded inputs, returning the
// decrypted plaintext as a string.
func OpenSealedBase64(sealedB64, publicKeyB64, privateKeyB64 string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		return "", fmt.Errorf("decode sealed box: %w", err)
	}
	publicKey, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode public key: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}

	plaintext, err := OpenSealed(sealed, publicKey, privateKey)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
