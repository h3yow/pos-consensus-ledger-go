// Package types defines the core data structures shared by all nodes:
// transactions, blocks, votes and slashing evidence.
package types

// Address is a 20-byte account / validator identifier.
type Address [20]byte

// Hash is a 32-byte SHA-256 digest.
type Hash [32]byte

// Validator is a member of the genesis validator set. PubKey is the
// 32-byte Ed25519 public key other nodes use to verify this validator's
// votes and block proposals; the corresponding private key never leaves
// the validator's own node.
type Validator struct {
	Addr   Address
	Stake  uint64
	PubKey []byte
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
//
// ProposerSig is the proposer's Ed25519 signature over the sig-excluding
// canonical block encoding (CanonicalBlock). It cryptographically binds the
// proposal to the scheduled validator: nobody else can produce a valid
// signature, and double-propose evidence is provably authored by that
// proposer on every node. The signature is excluded from the block hash so
// that block identity stays the hash of the signed payload (no malleability).
type Block struct {
	Height       uint64
	Proposer     Address
	PrevHash     Hash
	StateRoot    Hash
	Txs          []*Tx
	Evidence     []*Evidence
	JustifyVotes []*Vote // >=2/3 stake votes for the parent block
	ProposerSig  []byte
}

// Vote is a validator's vote that a block at Height/BlockHash is canonical.
// Sig is an Ed25519 signature by the voter over CanonicalVote, verifiable
// against the voter's PubKey published in the validator set.
type Vote struct {
	Height    uint64
	Voter     Address
	BlockHash Hash
	Sig       []byte
}
