package types

import (
	"crypto/ed25519"
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

// CanonicalVote encodes a vote deterministically.
func CanonicalVote(v *Vote) []byte {
	var e enc
	e.u64(v.Height)
	e.fixed(v.Voter[:])
	e.fixed(v.BlockHash[:])
	return e.b
}

// HashVote returns the canonical digest of a vote.
func HashVote(v *Vote) Hash { return HashBytes(CanonicalVote(v)) }

// SignVote fills in the vote signature with the voter's Ed25519 private key.
func SignVote(v *Vote, priv ed25519.PrivateKey) *Vote {
	v.Sig = ed25519.Sign(priv, CanonicalVote(v))
	return v
}

// VerifyVote checks the vote signature against the voter's PUBLIC key. The
// claimed voter address is also required to match the key's derived address,
// so a valid signature made by one validator can never be relabelled as
// another validator's vote.
func VerifyVote(v *Vote, pub ed25519.PublicKey) bool {
	if len(pub) != PubLen || len(v.Sig) != SigLen {
		return false
	}
	if AddressOf(pub) != v.Voter {
		return false
	}
	return ed25519.Verify(pub, CanonicalVote(v), v.Sig)
}

// CanonicalBlock encodes a block deterministically. Two blocks are "the same
// block" (safe under duplicate broadcast) iff their encodings are identical.
// The proposer signature is excluded: it authenticates the canonical payload.
func CanonicalBlock(b *Block) []byte {
	if b.canonicalBody != nil {
		return b.canonicalBody
	}
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

// HashBlock returns the canonical hash of a block. The proposer signature is
// not part of the canonical payload, so an honest re-broadcast of the same
// proposal and a signed copy share one hash.
func HashBlock(b *Block) Hash { return HashBytes(CanonicalBlock(b)) }

// SignBlock signs the canonical block payload with the proposer's private key.
func SignBlock(b *Block, priv ed25519.PrivateKey) *Block {
	b.Sig = ed25519.Sign(priv, CanonicalBlock(b))
	return b
}

// VerifyBlock checks the proposer signature against a PUBLIC key, and requires
// that the key is bound to the block's claimed proposer address.
func VerifyBlock(b *Block, pub ed25519.PublicKey) bool {
	if len(pub) != PubLen || len(b.Sig) != SigLen {
		return false
	}
	if AddressOf(pub) != b.Proposer {
		return false
	}
	return ed25519.Verify(pub, CanonicalBlock(b), b.Sig)
}

// AttachCanonicalBodyForEvidence records the exact canonical bytes on a block
// reconstructed from slashing evidence. Consensus verification code uses it;
// normal block production never calls this.
func AttachCanonicalBodyForEvidence(b *Block, body []byte) {
	cp := append([]byte(nil), body...)
	b.canonicalBody = cp
}
