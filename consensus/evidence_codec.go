package consensus

import (
	"encoding/binary"
	"fmt"

	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// signedVoteLen is the fixed size of an evidence vote payload:
// u64 height (8) | 20B voter | 32B blockHash | 64B Ed25519 signature = 124.
const signedVoteLen = 8 + 20 + 32 + 64

// signedVoteBytes encodes a vote together with its signature for evidence.
func signedVoteBytes(v *types.Vote) []byte {
	out := make([]byte, 0, signedVoteLen)
	out = append(out, types.CanonicalVote(v)...)
	out = append(out, v.Sig...)
	return out
}

// decodeVotePayload parses (canonical vote || 64-byte Ed25519 signature).
// Signature verification is separate (it needs the offender's public key).
func decodeVotePayload(b []byte) (*types.Vote, error) {
	if len(b) != signedVoteLen {
		return nil, fmt.Errorf("bad vote evidence length %d", len(b))
	}
	v := &types.Vote{Height: binary.BigEndian.Uint64(b[0:8])}
	copy(v.Voter[:], b[8:28])
	copy(v.BlockHash[:], b[28:60])
	v.Sig = make([]byte, 64)
	copy(v.Sig, b[60:124])
	return v, nil
}

// signedBlockBytes encodes (canonical block || signature || u32 sigLen) for
// use inside evidence. The trailing length lets any node split the signed
// message from the signature even though the block encoding is variable.
func signedBlockBytes(b *types.Block) []byte {
	cb := types.CanonicalBlock(b)
	out := make([]byte, 0, len(cb)+len(b.ProposerSig)+4)
	out = append(out, cb...)
	out = append(out, b.ProposerSig...)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b.ProposerSig)))
	out = append(out, l[:]...)
	return out
}

// decodeBlockPayload splits an evidence block payload into the canonical
// block bytes, the proposer signature and the parsed header prefix.
type decodedBlock struct {
	canonical []byte
	sig       []byte
	height    uint64
	proposer  types.Address
	hash      types.Hash
}

func decodeBlockPayload(b []byte) (decodedBlock, error) {
	var d decodedBlock
	if len(b) < 4 {
		return d, fmt.Errorf("bad block evidence length %d", len(b))
	}
	sigLen := binary.BigEndian.Uint32(b[len(b)-4:])
	// Layout: canonical | sig (sigLen) | u32 sigLen.
	if uint64(sigLen)+4 > uint64(len(b)) {
		return d, fmt.Errorf("block evidence signature length out of range")
	}
	canonical := b[:uint64(len(b))-4-uint64(sigLen)]
	if len(canonical) < 28 { // u64 height | 20B proposer
		return d, fmt.Errorf("block evidence canonical header too short")
	}
	d.canonical = canonical
	d.sig = b[len(canonical) : len(b)-4]
	d.height = binary.BigEndian.Uint64(canonical[0:8])
	copy(d.proposer[:], canonical[8:28])
	d.hash = types.HashBytes(canonical)
	return d, nil
}

// verifyEvidence checks that slashing evidence is well-formed AND genuinely
// authored by the claimed offender. Because every conflicting object carries
// an Ed25519 signature verified against the offender's genesis-published
// public key, nobody can forge evidence against an honest validator:
//   - "vote":    two signed votes by the same voter at one height for two
//     different block hashes, both signatures valid;
//   - "propose": two signed blocks at one height by the same proposer with
//     distinct hashes, both signatures valid.
//
// Any tampering, swapped offender field, unknown offender or mismatched
// height/identity is rejected identically on every node.
func (s *Set) verifyEvidence(ev *types.Evidence) error {
	if ev == nil {
		return ErrEvidenceInvalid
	}
	pub := s.PubKey(ev.Offender)
	if len(pub) == 0 {
		return ErrEvidenceInvalid // cannot authenticate an unknown offender
	}
	switch ev.Kind {
	case "vote":
		a, err1 := decodeVotePayload(ev.A)
		b, err2 := decodeVotePayload(ev.B)
		if err1 != nil || err2 != nil {
			return ErrEvidenceInvalid
		}
		if a.Voter != ev.Offender || b.Voter != ev.Offender {
			return ErrEvidenceInvalid
		}
		if a.Height != ev.Height || b.Height != ev.Height {
			return ErrEvidenceInvalid
		}
		if a.BlockHash == b.BlockHash {
			return ErrEvidenceInvalid
		}
		if !identity.VerifyVote(a, pub) || !identity.VerifyVote(b, pub) {
			return ErrEvidenceInvalid
		}
		return nil
	case "propose":
		a, err1 := decodeBlockPayload(ev.A)
		b, err2 := decodeBlockPayload(ev.B)
		if err1 != nil || err2 != nil {
			return ErrEvidenceInvalid
		}
		if a.height != ev.Height || b.height != ev.Height {
			return ErrEvidenceInvalid
		}
		if a.proposer != ev.Offender || b.proposer != ev.Offender {
			return ErrEvidenceInvalid
		}
		if a.hash == b.hash {
			return ErrEvidenceInvalid
		}
		if !identity.Verify(pub, a.canonical, a.sig) ||
			!identity.Verify(pub, b.canonical, b.sig) {
			return ErrEvidenceInvalid
		}
		return nil
	default:
		return ErrEvidenceInvalid
	}
}

// NewDoubleProposeEvidence builds slashing evidence for two distinct blocks
// at the same height signed by the same proposer.
func NewDoubleProposeEvidence(a, b *types.Block) (*types.Evidence, error) {
	if a == nil || b == nil || a.Height != b.Height || a.Proposer != b.Proposer {
		return nil, ErrEvidenceInvalid
	}
	if types.HashBlock(a) == types.HashBlock(b) {
		return nil, ErrEvidenceInvalid
	}
	return &types.Evidence{
		Kind:     "propose",
		Offender: a.Proposer,
		Height:   a.Height,
		A:        signedBlockBytes(a),
		B:        signedBlockBytes(b),
	}, nil
}

// VerifyEvidenceExport exposes evidence verification, using this validator
// set's public keys, to tests/external callers.
func (s *Set) VerifyEvidenceExport(ev *types.Evidence) error {
	return s.verifyEvidence(ev)
}
