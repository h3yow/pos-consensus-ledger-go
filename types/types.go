// Package types defines the core data structures shared by all nodes:
// transactions, blocks, votes and slashing evidence.
package types

import "crypto/ed25519"

// Address is a 20-byte account / validator identifier.
type Address [20]byte

// Hash is a 32-byte SHA-256 digest.
type Hash [32]byte

// Validator is a member of the genesis validator set. PubKey is the PUBLIC
// verification key other nodes use to authenticate the validator's votes and
// block proposals; Addr must equal AddressOf(PubKey). The validator set only
// ever carries public information — private keys never leave their owner node.
type Validator struct {
	Addr   Address
	Stake  uint64
	PubKey ed25519.PublicKey
}

// Tx is a simple value-transfer transaction with a per-sender nonce.
type Tx struct {
	From     Address
	To       Address
	Nonce    uint64
	Value    uint64
	Fee      uint64
	GasLimit uint64
	Data     []byte
	Sig      []byte // signature: sha256(canonical fields + From); nil => rejected
}

// Evidence proves a slashing offence:
//   - double propose: two distinct blocks at the same height by the same proposer
//   - double vote:    two distinct votes at the same height by the same voter
type Evidence struct {
	Kind     string // "propose" | "vote"
	Offender Address
	Height   uint64
	A, B     []byte // canonical encodings of the two conflicting objects
}

// Block is produced by the scheduled validator of a height. Sig is the
// proposer's Ed25519 signature over CanonicalBlock (the signature itself is
// excluded from the signed payload). It binds the proposal to the proposer's
// identity: double-propose evidence is only valid if BOTH conflicting blocks
// carry signatures that verify against the proposer's public key.
type Block struct {
	Height       uint64
	Proposer     Address
	PrevHash     Hash
	StateRoot    Hash
	Txs          []*Tx
	Evidence     []*Evidence
	JustifyVotes []*Vote // >=2/3 stake votes for the parent block
	Sig          []byte

	// canonicalBody is set only when a block is reconstructed from slashing
	// evidence: it is the exact original CanonicalBlock bytes, so hash and
	// signature verification run over the same bytes the proposer signed.
	// Regular block production leaves it nil.
	canonicalBody []byte
}

// Vote is a validator's vote that a block at Height/BlockHash is canonical.
// Sig is the voter's Ed25519 signature over CanonicalVote, verifiable against
// the voter's public key in the genesis validator set.
type Vote struct {
	Height    uint64
	Voter     Address
	BlockHash Hash
	Sig       []byte
}
