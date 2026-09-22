// Package types defines the core data structures of the PoS ledger:
// transactions, blocks, votes, equivocation evidence and network messages,
// together with deterministic encoding, hashing and signature helpers.
//
// All encodings are length-prefixed and big-endian so that the same
// logical object always yields identical bytes (and therefore identical
// hashes) on every node.
package types

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// AddressLen is the length (in bytes) of an account/validator address.
const AddressLen = 20

// Address is the first AddressLen bytes of SHA-256 over an ed25519 public key.
type Address [AddressLen]byte

func (a Address) String() string { return fmt.Sprintf("%x", a[:]) }

// Cmp compares addresses lexicographically: -1, 0 or 1.
func (a Address) Cmp(o Address) int { return bytes.Compare(a[:], o[:]) }

// Hash is a SHA-256 digest.
type Hash [32]byte

func (h Hash) String() string { return fmt.Sprintf("%x", h[:8]) }

// Cmp compares hashes lexicographically: -1, 0 or 1.
func (h Hash) Cmp(o Hash) int { return bytes.Compare(h[:], o[:]) }

// ZeroHash is the all-zero hash used as parent hash of the genesis block.
var ZeroHash Hash

// PutUint64 appends a big-endian uint64 to b.
func PutUint64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

// PutUint32 appends a big-endian uint32 to b.
func PutUint32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

// PutByte appends a single byte.
func PutByte(b []byte, v byte) []byte { return append(b, v) }

// PutHash appends a hash's raw bytes.
func PutHash(b []byte, h Hash) []byte { return append(b, h[:]...) }

// PutAddr appends an address's raw bytes.
func PutAddr(b []byte, a Address) []byte { return append(b, a[:]...) }

// PutBytes length-prefixes and appends raw bytes.
func PutBytes(b, raw []byte) []byte {
	b = PutUint64(b, uint64(len(raw)))
	return append(b, raw...)
}

// HashFromBytes returns SHA-256 of the input.
func HashFromBytes(b []byte) Hash { return sha256.Sum256(b) }

// AddressFromPub derives an Address from an ed25519 public key.
func AddressFromPub(pub ed25519.PublicKey) Address {
	h := HashFromBytes(pub)
	var a Address
	copy(a[:], h[:AddressLen])
	return a
}

func verify(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
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
func (t *Tx) SigningBytes() []byte {
	b := []byte("tx/v1:")
	b = PutBytes(b, t.FromPub)
	b = PutAddr(b, t.To)
	b = PutUint64(b, t.Nonce)
	b = PutUint64(b, t.Amount)
	b = PutUint64(b, t.Fee)
	return b
}

// Hash returns the content hash of the signed transaction.
func (t *Tx) Hash() Hash {
	b := t.SigningBytes()
	b = PutBytes(b, t.Sig)
	return HashFromBytes(b)
}

// VerifySig checks the ed25519 signature.
func (t *Tx) VerifySig() bool {
	return verify(t.FromPub, t.SigningBytes(), t.Sig)
}

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

// HeaderBytes is the deterministic byte sequence that the proposer signs
// and that identifies the block. The transaction set is committed via
// TxRoot and the evidence set via the sorted list of evidence hashes.
func (b *Block) HeaderBytes() []byte {
	evHashes := make([]Hash, 0, len(b.Evidence))
	for _, ev := range b.Evidence {
		evHashes = append(evHashes, ev.Hash())
	}
	sortHashes(evHashes)

	out := []byte("block/v1:")
	out = PutUint64(out, b.Height)
	out = PutUint32(out, b.Round)
	out = PutHash(out, b.ParentHash)
	out = PutBytes(out, b.ProposerPub)
	out = PutHash(out, b.TxRoot)
	out = PutHash(out, b.StateRoot)
	out = PutUint64(out, b.Timestamp)
	out = PutUint64(out, uint64(len(evHashes)))
	for _, h := range evHashes {
		out = PutHash(out, h)
	}
	return out
}

// Hash is the SHA-256 hash of the signed header bytes.
func (b *Block) Hash() Hash { return HashFromBytes(b.HeaderBytes()) }

// VerifySig checks the proposer signature against the embedded public key,
// and that Proposer matches the public key's address.
func (b *Block) VerifySig() bool {
	if AddressFromPub(b.ProposerPub) != b.Proposer {
		return false
	}
	return verify(b.ProposerPub, b.HeaderBytes(), b.Sig)
}

// Clone returns a deep copy of the block.
func (b *Block) Clone() *Block {
	c := *b
	c.ProposerPub = append(ed25519.PublicKey(nil), b.ProposerPub...)
	c.Sig = append([]byte(nil), b.Sig...)
	c.ParentHash = b.ParentHash
	c.TxRoot = b.TxRoot
	c.StateRoot = b.StateRoot
	c.Txs = make([]*Tx, len(b.Txs))
	for i, t := range b.Txs {
		tc := *t
		tc.FromPub = append(ed25519.PublicKey(nil), t.FromPub...)
		tc.Sig = append([]byte(nil), t.Sig...)
		c.Txs[i] = &tc
	}
	c.Evidence = make([]*Evidence, len(b.Evidence))
	for i, e := range b.Evidence {
		c.Evidence[i] = e.clone()
	}
	return &c
}

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
func (v *Vote) SigningBytes() []byte {
	out := []byte("vote/v1:")
	out = PutUint64(out, v.Height)
	out = PutUint32(out, v.Round)
	out = PutHash(out, v.BlockHash)
	out = PutBytes(out, v.VoterPub)
	return out
}

// Hash identifies the vote content.
func (v *Vote) Hash() Hash {
	out := v.SigningBytes()
	out = PutBytes(out, v.Sig)
	return HashFromBytes(out)
}

// VerifySig checks the voter signature and address match.
func (v *Vote) VerifySig() bool {
	if AddressFromPub(v.VoterPub) != v.Voter {
		return false
	}
	return verify(v.VoterPub, v.SigningBytes(), v.Sig)
}

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

func (e *Evidence) clone() *Evidence {
	c := *e
	if e.BlockA != nil {
		c.BlockA = e.BlockA.Clone()
	}
	if e.BlockB != nil {
		c.BlockB = e.BlockB.Clone()
	}
	if e.VoteA != nil {
		v := *e.VoteA
		v.VoterPub = append(ed25519.PublicKey(nil), e.VoteA.VoterPub...)
		v.Sig = append([]byte(nil), e.VoteA.Sig...)
		c.VoteA = &v
	}
	if e.VoteB != nil {
		v := *e.VoteB
		v.VoterPub = append(ed25519.PublicKey(nil), e.VoteB.VoterPub...)
		v.Sig = append([]byte(nil), e.VoteB.Sig...)
		c.VoteB = &v
	}
	return &c
}

// Hash deterministically identifies the evidence by kind/slot and the
// hashes of the two conflicting signed objects.
func (e *Evidence) Hash() Hash {
	var ha, hb Hash
	out := []byte("evidence/v1:")
	out = PutByte(out, e.Kind)
	out = PutUint64(out, e.Height)
	out = PutUint32(out, e.Round)
	switch e.Kind {
	case EquivocationBlock:
		if e.BlockA != nil {
			ha = e.BlockA.Hash()
		}
		if e.BlockB != nil {
			hb = e.BlockB.Hash()
		}
	case EquivocationVote:
		if e.VoteA != nil {
			ha = e.VoteA.Hash()
		}
		if e.VoteB != nil {
			hb = e.VoteB.Hash()
		}
	}
	if ha.Cmp(hb) > 0 {
		ha, hb = hb, ha
	}
	out = PutHash(out, ha)
	out = PutHash(out, hb)
	return HashFromBytes(out)
}

// Validator checks embedded signatures and the conflict rule.
func (e *Evidence) Validator() bool {
	switch e.Kind {
	case EquivocationBlock:
		a, b := e.BlockA, e.BlockB
		if a == nil || b == nil || !a.VerifySig() || !b.VerifySig() {
			return false
		}
		if a.Proposer != b.Proposer {
			return false
		}
		if a.Height != e.Height || b.Height != e.Height ||
			a.Round != e.Round || b.Round != e.Round {
			return false
		}
		return a.Hash() != b.Hash()
	case EquivocationVote:
		a, b := e.VoteA, e.VoteB
		if a == nil || b == nil || !a.VerifySig() || !b.VerifySig() {
			return false
		}
		if a.Voter != b.Voter {
			return false
		}
		if a.Height != e.Height || b.Height != e.Height ||
			a.Round != e.Round || b.Round != e.Round {
			return false
		}
		return a.BlockHash != b.BlockHash
	default:
		return false
	}
}

// Offender returns the address of the misbehaving validator.
func (e *Evidence) Offender() Address {
	switch e.Kind {
	case EquivocationBlock:
		if e.BlockA != nil {
			return e.BlockA.Proposer
		}
	case EquivocationVote:
		if e.VoteA != nil {
			return e.VoteA.Voter
		}
	}
	return Address{}
}

// sortHashes orders hashes ascending in place.
func sortHashes(hs []Hash) {
	for i := 1; i < len(hs); i++ {
		for j := i; j > 0 && hs[j-1].Cmp(hs[j]) > 0; j-- {
			hs[j-1], hs[j] = hs[j], hs[j-1]
		}
	}
}

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
