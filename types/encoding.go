package types

import (
	"crypto/sha256"
	"encoding/binary"
)

// enc is a tiny deterministic, length-prefixed byte builder. Every field of
// variable length is prefixed with a uint32 length, so identical structures
// always produce identical bytes on every node.
type enc struct{ b []byte }

func (e *enc) u64(v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	e.b = append(e.b, buf[:]...)
}

func (e *enc) u32(v uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	e.b = append(e.b, buf[:]...)
}

func (e *enc) bytes(p []byte) {
	e.u32(uint32(len(p)))
	e.b = append(e.b, p...)
}

func (e *enc) fixed(p []byte) { e.b = append(e.b, p...) }

func (e *enc) str(s string) { e.bytes([]byte(s)) }

// HashBytes computes the SHA-256 digest of b.
func HashBytes(b []byte) Hash { return sha256.Sum256(b) }

// CanonicalTx encodes the signed fields of a transaction (signature excluded).
func CanonicalTx(t *Tx) []byte {
	var e enc
	e.fixed(t.From[:])
	e.fixed(t.To[:])
	e.u64(t.Nonce)
	e.u64(t.Value)
	e.u64(t.Fee)
	e.u64(t.GasLimit)
	e.bytes(t.Data)
	return e.b
}

// SignTx sets Sig to SHA-256 over the canonical encoding. This stands in for
// a real signature in the simulation: From is part of the signed payload, so
// a tampered transaction fails verification.
func SignTx(t *Tx) *Tx {
	d := HashBytes(CanonicalTx(t))
	t.Sig = d[:]
	return t
}

// VerifyTx checks the signature against the canonical encoding.
func VerifyTx(t *Tx) bool {
	if len(t.Sig) != 32 {
		return false
	}
	want := HashBytes(CanonicalTx(t))
	return string(t.Sig) == string(want[:])
}

// CanonicalVote encodes a vote deterministically. The bytes are the message
// that the voter signs with its Ed25519 private key (see identity.SignVote).
func CanonicalVote(v *Vote) []byte {
	var e enc
	e.u64(v.Height)
	e.fixed(v.Voter[:])
	e.fixed(v.BlockHash[:])
	return e.b
}

// HashVote returns the canonical digest of a vote.
func HashVote(v *Vote) Hash { return HashBytes(CanonicalVote(v)) }

// CanonicalBlock encodes a block deterministically, EXCLUDING ProposerSig.
// Two blocks are "the same block" (safe under duplicate broadcast) iff their
// encodings are identical. The same bytes are signed by the proposer
// (identity.SignBlock), so the block hash commits to exactly the signed
// payload; the signature itself cannot be malleated into a second identity.
func CanonicalBlock(b *Block) []byte {
	var e enc
	e.u64(b.Height)
	e.fixed(b.Proposer[:])
	e.fixed(b.PrevHash[:])
	e.fixed(b.StateRoot[:])

	e.u32(uint32(len(b.Txs)))
	for _, t := range b.Txs {
		e.bytes(CanonicalTx(t))
		e.bytes(t.Sig)
	}

	e.u32(uint32(len(b.Evidence)))
	for _, ev := range b.Evidence {
		e.str(ev.Kind)
		e.fixed(ev.Offender[:])
		e.u64(ev.Height)
		e.bytes(ev.A)
		e.bytes(ev.B)
	}

	e.u32(uint32(len(b.JustifyVotes)))
	for _, v := range b.JustifyVotes {
		e.bytes(CanonicalVote(v))
		e.bytes(v.Sig)
	}
	return e.b
}

// HashBlock returns the canonical hash of a block.
func HashBlock(b *Block) Hash { return HashBytes(CanonicalBlock(b)) }
