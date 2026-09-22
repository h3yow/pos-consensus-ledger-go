package consensus

import (
	"encoding/binary"
	"fmt"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// signedVoteBytes encodes a vote together with its signature for evidence.
func signedVoteBytes(v *types.Vote) []byte {
	out := make([]byte, 0, len(types.CanonicalVote(v))+len(v.Sig))
	out = append(out, types.CanonicalVote(v)...)
	out = append(out, v.Sig...)
	return out
}

// decodeVotePayload parses (canonical vote || 32-byte signature).
func decodeVotePayload(b []byte) (*types.Vote, error) {
	if len(b) != 60+32 {
		return nil, fmt.Errorf("bad vote evidence length %d", len(b))
	}
	v := &types.Vote{Height: binary.BigEndian.Uint64(b[0:8])}
	copy(v.Voter[:], b[8:28])
	copy(v.BlockHash[:], b[28:60])
	v.Sig = make([]byte, 32)
	copy(v.Sig, b[60:92])
	if !types.VerifyVote(v) {
		return nil, fmt.Errorf("vote evidence signature invalid")
	}
	return v, nil
}

// decodeBlockHeader parses only the fixed-size prefix of CanonicalBlock:
// u64 height | 20B proposer | 32B prevHash | 32B stateRoot (92 bytes).
func decodeBlockHeader(b []byte) decodedHeader {
	if len(b) < 92 {
		return decodedHeader{}
	}
	d := decodedHeader{ok: true, height: binary.BigEndian.Uint64(b[0:8])}
	copy(d.proposer[:], b[8:28])
	d.hash = types.HashBytes(b)
	return d
}

// NewDoubleProposeEvidence builds slashing evidence for two distinct blocks
// at the same height signed by the same proposer.
func NewDoubleProposeEvidence(a, b *types.Block) (*types.Evidence, error) {
	if a == nil || b == nil || a.Height != b.Height || a.Proposer != b.Proposer {
		return nil, ErrEvidenceInvalid
	}
	ra, rb := types.CanonicalBlock(a), types.CanonicalBlock(b)
	if types.HashBlock(a) == types.HashBlock(b) {
		return nil, ErrEvidenceInvalid
	}
	return &types.Evidence{
		Kind:     "propose",
		Offender: a.Proposer,
		Height:   a.Height,
		A:        ra,
		B:        rb,
	}, nil
}

// VerifyEvidenceExport exposes evidence verification to tests/external callers.
func VerifyEvidenceExport(ev *types.Evidence) error { return verifyEvidence(ev) }
