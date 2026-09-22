// Package types defines the core data structures of the PoS ledger:
// transactions, blocks, votes, equivocation evidence and network messages,
// together with deterministic encoding, hashing and signature helpers.
package types

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
)

// AddressLen is the length (in bytes) of an account/validator address.
const AddressLen = 20

// Address is the first AddressLen bytes of SHA-256 over an ed25519 public key.
type Address [AddressLen]byte

func (a Address) String() string { return fmt.Sprintf("%x", a[:]) }

// Hash is a SHA-256 digest.
type Hash [32]byte

func (h Hash) String() string { return fmt.Sprintf("%x", h[:8]) }

// ZeroHash is the all-zero hash used as parent hash of the genesis block.
var ZeroHash Hash

// PutUint64 appends a big-endian uint64 to b.
func PutUint64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

// PutBytes length-prefixes and appends raw bytes.
func PutBytes(b, raw []byte) []byte {
	b = PutUint64(b, uint64(len(raw)))
	return append(b, raw...)
}

// AddressFromPub derives an Address from an ed25519 public key.
func AddressFromPub(pub ed25519.PublicKey) Address {
	h := HashFromBytes(pub)
	var a Address
	copy(a[:], h[:AddressLen])
	return a
}

// HashFromBytes returns SHA-256 of the input.
func HashFromBytes(b []byte) Hash {
	panic("not implemented")
}

// Tx is a signed balance-transfer transaction.
type Tx struct {
	FromPub ed25519.PublicKey
	To      Address
	Nonce   uint64
	Amount  uint64
	Fee     uint64
	Sig     []byte
}

// Sender returns the address derived from the embedded public key.
func (t *Tx) Sender() Address { return AddressFromPub(t.FromPub) }

// SigningBytes returns the canonical byte sequence that the sender signs.
func (t *Tx) SigningBytes() []byte { panic("not implemented") }

// Hash returns the content hash of the signed transaction.
func (t *Tx) Hash() Hash { panic("not implemented") }

// VerifySig checks the ed25519 signature.
func (t *Tx) VerifySig() bool { panic("not implemented") }

// Block is a proposal at a given height/round by a validator.
type Block struct {
	Height      uint64
	Round       uint32
	ParentHash  Hash
	Proposer    Address
	ProposerPub ed25519.PublicKey
	TxRoot      Hash
	StateRoot   Hash
	Txs         []*Tx
	Evidence    []*Evidence
	Timestamp   uint64
	Sig         []byte
}

// HeaderBytes is the deterministic byte sequence that the proposer signs.
func (b *Block) HeaderBytes() []byte { panic("not implemented") }

// Hash is the SHA-256 hash of the signed header bytes.
func (b *Block) Hash() Hash { panic("not implemented") }

// VerifySig checks the proposer signature against the embedded public key.
func (b *Block) VerifySig() bool { panic("not implemented") }

// Clone returns a deep copy of the block.
func (b *Block) Clone() *Block { panic("not implemented") }

// Vote is a validator's endorsement of a block at a given height/round.
type Vote struct {
	Height    uint64
	Round     uint32
	BlockHash Hash
	Voter     Address
	VoterPub  ed25519.PublicKey
	Sig       []byte
}

// SigningBytes returns the canonical byte sequence the voter signs.
func (v *Vote) SigningBytes() []byte { panic("not implemented") }

// Hash identifies the vote content.
func (v *Vote) Hash() Hash { panic("not implemented") }

// VerifySig checks the voter signature.
func (v *Vote) VerifySig() bool { panic("not implemented") }

// Equivocation kind tags for Evidence.
const (
	EquivocationBlock = 1 // two blocks by same proposer at same height/round
	EquivocationVote  = 2 // two votes by same voter at same height/round
)

// Evidence proves a validator signed two conflicting objects.
type Evidence struct {
	Kind   uint8
	Height uint64
	Round  uint32

	BlockA *Block
	BlockB *Block
	VoteA  *Vote
	VoteB  *Vote
}

// Hash deterministically identifies the evidence.
func (e *Evidence) Hash() Hash { panic("not implemented") }

// Validator checks embedded signatures and the conflict rule.
func (e *Evidence) Validator() bool { panic("not implemented") }

// Offender returns the address of the misbehaving validator.
func (e *Evidence) Offender() Address { panic("not implemented") }

// Validator is a consensus participant with stake weight.
type Validator struct {
	Address Address
	PubKey  ed25519.PublicKey
	Weight  uint64
}

// Genesis configures the initial chain: balances, validators, treasury.
type Genesis struct {
	ChainID              string
	Balances             map[Address]uint64
	Validators           []*Validator
	Treasury             Address
	SlashRateNumerator   uint64
	SlashRateDenominator uint64
	ThresholdNumerator   uint64
	ThresholdDenominator uint64
}

// MsgKind enumerates the in-process network message types.
type MsgKind uint8

const (
	MsgTx MsgKind = iota + 1
	MsgBlock
	MsgVote
)

// Message is an envelope broadcast between simulated nodes.
type Message struct {
	Kind  MsgKind
	From  string
	Tx    *Tx
	Block *Block
	Vote  *Vote
}
