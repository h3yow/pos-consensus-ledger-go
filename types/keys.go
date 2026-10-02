// Package types: validator identity keys.
//
// Consensus messages (votes and block proposals) are authenticated with real
// Ed25519 keypairs (Go standard library, no external dependencies):
//
//   - every validator's PUBLIC key is part of the genesis validator set and is
//     the only information other nodes need to verify "who sent this";
//   - each node holds only its OWN private key and signs with it;
//   - an Address is derived deterministically from the public key
//     (Address = last 20 bytes of SHA-256(pub)), so an address and the key
//     that may speak for it are cryptographically bound. A party lacking a
//     validator's private key can neither forge its votes nor assemble signed
//     double-propose evidence that frames it.
package types

import (
	"crypto/ed25519"
	"crypto/sha256"
)

// SigLen is the fixed length of an Ed25519 signature.
const SigLen = ed25519.SignatureSize // 64

// PubLen is the fixed length of an Ed25519 public key.
const PubLen = ed25519.PublicKeySize // 32

// GenerateKey derives a deterministic private key from seedMaterial.
// Tests/demo use it to make runs reproducible without a real key store;
// production startup would instead load the node's own private key from its
// configured key material.
func GenerateKey(seedMaterial []byte) ed25519.PrivateKey {
	s := sha256.Sum256(seedMaterial)
	return ed25519.NewKeyFromSeed(s[:])
}

// AddressOf derives the on-chain address bound to a public key.
func AddressOf(pub ed25519.PublicKey) Address {
	d := sha256.Sum256(pub)
	var a Address
	copy(a[:], d[len(d)-AddressLen:])
	return a
}

// AddressLen is the address size in bytes.
const AddressLen = 20
