// Package identity provides the real asymmetric key layer that binds
// validator identities to their votes and block proposals.
//
// It uses Ed25519 from the Go standard library (crypto/ed25519): the private
// key stays on the validator's own node, only the 32-byte public key is
// published in the genesis validator set. Every other node independently
// verifies signatures against that public key, so a party lacking a
// validator's private key can neither forge its votes nor assemble evidence
// that appears to be its double signing.
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// SeedFor derives the deterministic 32-byte Ed25519 seed for an address.
// In a real deployment the seed would be an independently generated secret;
// deriving it from a fixed domain-separated preimage here keeps the local
// simulation reproducible with no key files, while still being a genuine
// asymmetric keypair (knowledge of the address/public key reveals nothing
// that allows signing).
func SeedFor(addr types.Address) []byte {
	d := sha256.New()
	d.Write([]byte("pos-ledger/ed25519-seed/v1:"))
	d.Write(addr[:])
	return d.Sum(nil)
}

// PrivateKeyFor derives this node's Ed25519 private key for addr.
func PrivateKeyFor(addr types.Address) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(SeedFor(addr))
}

// PublicKeyFor returns the 32-byte public key bound to addr.
func PublicKeyFor(addr types.Address) []byte {
	priv := PrivateKeyFor(addr)
	pub := priv.Public().(ed25519.PublicKey)
	out := make([]byte, ed25519.PublicKeySize)
	copy(out, pub)
	return out
}

// Sign produces an Ed25519 signature over msg with priv.
func Sign(priv ed25519.PrivateKey, msg []byte) []byte {
	sig := ed25519.Sign(priv, msg)
	out := make([]byte, len(sig))
	copy(out, sig)
	return out
}

// Verify checks sig over msg against a 32-byte validator public key.
// Malformed keys or signatures are rejected rather than panicking.
func Verify(pub, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// SignVote signs a validator vote and fills in its Sig field with the
// voter's Ed25519 signature. Returns v for chaining.
func SignVote(v *types.Vote, priv ed25519.PrivateKey) *types.Vote {
	v.Sig = Sign(priv, types.CanonicalVote(v))
	return v
}

// VerifyVote independently checks a vote against the voter's public key.
// This is the only way a vote is accepted: a party without the voter's
// private key cannot produce a passing signature.
func VerifyVote(v *types.Vote, pub []byte) bool {
	if v == nil {
		return false
	}
	return Verify(pub, types.CanonicalVote(v), v.Sig)
}

// SignBlock signs the sig-excluding canonical block encoding with the
// proposer's private key and fills ProposerSig.
func SignBlock(b *types.Block, priv ed25519.PrivateKey) *types.Block {
	b.ProposerSig = Sign(priv, types.CanonicalBlock(b))
	return b
}

// VerifyBlock checks the proposer signature against the proposer's public
// key. The genesis block (zero proposer, empty signature) is accepted as
// implicitly signed by the protocol bootstrap.
func VerifyBlock(b *types.Block, pub []byte) bool {
	if b == nil {
		return false
	}
	if b.Height == 0 && b.Proposer == (types.Address{}) {
		return len(b.ProposerSig) == 0
	}
	return Verify(pub, types.CanonicalBlock(b), b.ProposerSig)
}
