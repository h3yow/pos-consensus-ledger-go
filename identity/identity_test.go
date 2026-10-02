package identity

import (
	"crypto/ed25519"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	var a types.Address
	a[19] = 5
	priv := PrivateKeyFor(a)
	pub := PublicKeyFor(a)
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("pub len = %d", len(pub))
	}
	msg := []byte("canonical payload")
	sig := Sign(priv, msg)
	if !Verify(pub, msg, sig) {
		t.Fatal("valid signature must verify")
	}
	// Wrong message fails.
	if Verify(pub, []byte("tampered"), sig) {
		t.Fatal("tampered message must fail")
	}
	// A key derived for a different address must not validate the signature.
	var b types.Address
	b[19] = 6
	if Verify(PublicKeyFor(b), msg, sig) {
		t.Fatal("signature under another identity must not verify")
	}
	// Malformed key/signature rejected, no panic.
	if Verify(nil, msg, sig) || Verify(pub, msg, nil) {
		t.Fatal("malformed inputs must be rejected")
	}
	t.Log("decision: Ed25519 sign/verify binds payload to the derived identity; cross-key and malformed fail")
}
