package enc

import (
	"encoding/base64"
	"testing"
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
