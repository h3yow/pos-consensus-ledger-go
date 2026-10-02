package consensus

import (
	"encoding/binary"
	"fmt"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// frame prefixes a payload with its uint32 length, so signed consensus
// messages embedded in evidence are unambiguously decodable on every node.
func frame(payload []byte) []byte {
	out := make([]byte, 0, 4+len(payload))
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(payload)))
	out = append(out, l[:]...)
	out = append(out, payload...)
	return out
}

func unframe(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("frame too short")
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n != len(b)-4 {
		return nil, fmt.Errorf("frame length mismatch")
	}
	return b[4:], nil
}

// signedVoteBytes is the evidence wire form of a signed vote:
// frame(canonical vote || ed25519 signature).
func signedVoteBytes(v *types.Vote) []byte {
	out := make([]byte, 0, len(types.CanonicalVote(v))+len(v.Sig))
	out = append(out, types.CanonicalVote(v)...)
	out = append(out, v.Sig...)
	return frame(out)
}

// decodeSignedVote parses frame(canonical vote || signature).
func decodeSignedVote(b []byte) (*types.Vote, error) {
	payload, err := unframe(b)
	if err != nil {
		return nil, err
	}
	if len(payload) != 60+types.SigLen {
		return nil, fmt.Errorf("bad vote evidence length %d", len(payload))
	}
	v := &types.Vote{Height: binary.BigEndian.Uint64(payload[0:8])}
	copy(v.Voter[:], payload[8:28])
	copy(v.BlockHash[:], payload[28:60])
	v.Sig = append([]byte(nil), payload[60:60+types.SigLen]...)
	return v, nil
}

// signedBlockBytes is the evidence wire form of a signed proposal:
// frame(canonical block || ed25519 signature).
func signedBlockBytes(b *types.Block) []byte {
	out := make([]byte, 0, 0)
	out = append(out, types.CanonicalBlock(b)...)
	out = append(out, b.Sig...)
	return frame(out)
}

// decodeSignedBlock parses frame(canonical block || signature). The trailing
// fixed-size signature is split off and the remainder is the exact canonical
// block encoding, so HashBlock recomputes the proposal hash.
func decodeSignedBlock(b []byte) (*types.Block, error) {
	payload, err := unframe(b)
	if err != nil {
		return nil, err
	}
	if len(payload) <= types.SigLen {
		return nil, fmt.Errorf("bad block evidence length %d", len(payload))
	}
	body := payload[:len(payload)-types.SigLen]
	sig := payload[len(payload)-types.SigLen:]
	if len(body) < 92 {
		return nil, fmt.Errorf("block header truncated")
	}
	blk := &types.Block{Height: binary.BigEndian.Uint64(body[0:8])}
	copy(blk.Proposer[:], body[8:28])
	copy(blk.PrevHash[:], body[28:60])
	copy(blk.StateRoot[:], body[60:92])
	blk.Sig = append([]byte(nil), sig...)
	// VerifyBlock/HashBlock run over the exact canonical body the proposer
	// originally signed.
	types.AttachCanonicalBodyForEvidence(blk, body)
	return blk, nil
}

// NewDoubleProposeEvidence builds slashing evidence for two distinct, SIGNED
// blocks at the same height by the same proposer. Both signatures are checked
// by verifyEvidence against the proposer's public key at block validation.
func NewDoubleProposeEvidence(a, b *types.Block) (*types.Evidence, error) {
	if a == nil || b == nil || a.Height != b.Height || a.Proposer != b.Proposer {
		return nil, ErrEvidenceInvalid
	}
	if len(a.Sig) != types.SigLen || len(b.Sig) != types.SigLen {
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

// VerifyEvidenceExport exposes evidence verification to tests/external callers.
func VerifyEvidenceExport(s *Set, ev *types.Evidence) error {
	return verifyEvidence(s, ev)
}
