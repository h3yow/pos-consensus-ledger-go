// Package types defines the core data structures shared by all nodes:
// transactions, blocks, votes and slashing evidence.
package types

// Address is a 20-byte account / validator identifier.
type Address [20]byte

// Hash is a 32-byte SHA-256 digest.
type Hash [32]byte

// Validator is a member of the genesis validator set.
type Validator struct {
	Addr  Address
	Stake uint64
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

// Block is produced by the scheduled validator of a height.
type Block struct {
	Height       uint64
	Proposer     Address
	PrevHash     Hash
	StateRoot    Hash
	Txs          []*Tx
	Evidence     []*Evidence
	JustifyVotes []*Vote // >=2/3 stake votes for the parent block
}

// Vote is a validator's vote that a block at Height/BlockHash is canonical.
type Vote struct {
	Height    uint64
	Voter     Address
	BlockHash Hash
	Sig       []byte
}
