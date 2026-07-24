package enc

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/nacl/box"
)

func TestGenerateKeypair(t *testing.T) {
	kp, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}

	for name, val := range map[string]string{"public": kp.PublicKey, "private": kp.PrivateKey} {
		raw, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			t.Errorf("%s key is not valid base64: %v", name, err)
		}
		if len(raw) != 32 {
			t.Errorf("%s key is %d bytes, want 32", name, len(raw))
		}
	}

	if kp.PublicKey == kp.PrivateKey {
		t.Error("public and private key must differ")
	}

	other, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if other.PrivateKey == kp.PrivateKey {
		t.Error("two generated keypairs must not be identical")
	}
}

func TestOpenSealedRoundTrip(t *testing.T) {
	kp, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	pub, _ := base64.StdEncoding.DecodeString(kp.PublicKey)
	priv, _ := base64.StdEncoding.DecodeString(kp.PrivateKey)

	message := []byte("the secret prompt")
	plaintext, err := OpenSealed(sealForTest(t, message, pub), pub, priv)
	if err != nil {
		t.Fatalf("OpenSealed: %v", err)
	}
	if string(plaintext) != string(message) {
		t.Errorf("round-trip = %q, want %q", plaintext, message)
	}
}

func TestOpenSealedWrongKeyFails(t *testing.T) {
	recipient, _ := GenerateKeypair()
	pub, _ := base64.StdEncoding.DecodeString(recipient.PublicKey)

	other, _ := GenerateKeypair()
	otherPub, _ := base64.StdEncoding.DecodeString(other.PublicKey)
	otherPriv, _ := base64.StdEncoding.DecodeString(other.PrivateKey)

	if _, err := OpenSealed(sealForTest(t, []byte("secret"), pub), otherPub, otherPriv); nil == err {
		t.Error("expected an error opening a sealed box with the wrong keypair")
	}
}

// sealForTest mirrors libsodium's crypto_box_seal: an ephemeral public key
// prefix and a nonce derived as blake2b(ephemeral_pub || recipient_pub).
func sealForTest(t *testing.T, message, recipientPub []byte) []byte {
	t.Helper()

	ephemeralPub, ephemeralPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var rpk [32]byte
	copy(rpk[:], recipientPub)

	hash, err := blake2b.New(24, nil)
	if err != nil {
		t.Fatalf("blake2b: %v", err)
	}
	hash.Write(ephemeralPub[:])
	hash.Write(rpk[:])
	var nonce [24]byte
	copy(nonce[:], hash.Sum(nil))

	return append(ephemeralPub[:], box.Seal(nil, message, &nonce, &rpk, ephemeralPriv)...)
}
