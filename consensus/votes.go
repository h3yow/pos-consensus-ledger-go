package consensus

import (
	"bytes"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// voteRecord keeps a stored vote and its evidence payload.
type voteRecord struct {
	vote *types.Vote
	raw  []byte // canonical encoding, for evidence construction
}

// VotePool keeps votes per height and detects equivocation.
type VotePool struct {
	// byHeight maps height -> voter -> their recorded vote.
	byHeight map[uint64]map[types.Address]*voteRecord
	order    map[uint64][]*types.Vote // insertion order, deterministic replay
}

// NewVotePool creates an empty vote pool.
func NewVotePool() *VotePool {
	return &VotePool{
		byHeight: make(map[uint64]map[types.Address]*voteRecord),
		order:    make(map[uint64][]*types.Vote),
	}
}

// AddVote records a vote. Rules:
//   - signature must verify and the voter must claim itself consistently;
//   - an identical repeat (same height, same block hash) is a harmless
//     re-broadcast: no error, no evidence;
//   - a second vote for a DIFFERENT block hash at the same height is
//     equivocation. The conflicting vote is retained and evidence is
//     returned so a proposer can include it to slash the offender.
func (p *VotePool) AddVote(v *types.Vote) (*types.Evidence, error) {
	if !types.VerifyVote(v) {
		return nil, ErrBadVote
	}
	at := p.byHeight[v.Height]
	if at == nil {
		at = make(map[types.Address]*voteRecord)
		p.byHeight[v.Height] = at
	}
	raw := signedVoteBytes(v)
	if rec, ok := at[v.Voter]; ok {
		if rec.vote.BlockHash == v.BlockHash {
			return nil, nil // duplicate broadcast of the same vote
		}
		return &types.Evidence{
			Kind:     "vote",
			Offender: v.Voter,
			Height:   v.Height,
			A:        rec.raw,
			B:        raw,
		}, ErrDoubleVote
	}
	at[v.Voter] = &voteRecord{vote: v, raw: raw}
	p.order[v.Height] = append(p.order[v.Height], v)
	return nil, nil
}

// VotesAt returns all distinct votes stored for height h in arrival order.
func (p *VotePool) VotesAt(h uint64) []*types.Vote {
	out := make([]*types.Vote, len(p.order[h]))
	copy(out, p.order[h])
	return out
}

// HasVoted reports whether the voter already voted at height h.
func (p *VotePool) HasVoted(h uint64, voter types.Address) bool {
	_, ok := p.byHeight[h][voter]
	return ok
}

// verifyEvidence checks that slashing evidence is well-formed and really
// proves equivocation:
//   - "vote": two canonical votes by the same voter at one height for two
//     different block hashes, both with valid signatures;
//   - "propose": two canonical blocks at one height by the same proposer
//     with distinct hashes.
func verifyEvidence(ev *types.Evidence) error {
	if ev == nil {
		return ErrEvidenceInvalid
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
		return nil
	case "propose":
		// Canonical block payloads must differ and must share height/proposer.
		if len(ev.A) == 0 || len(ev.B) == 0 || bytes.Equal(ev.A, ev.B) {
			return ErrEvidenceInvalid
		}
		a := decodeBlockHeader(ev.A)
		b := decodeBlockHeader(ev.B)
		if !a.ok || !b.ok {
			return ErrEvidenceInvalid
		}
		if a.height != ev.Height || b.height != ev.Height {
			return ErrEvidenceInvalid
		}
		if a.proposer != ev.Offender || b.proposer != ev.Offender {
			return ErrEvidenceInvalid
		}
		if bytes.Equal(a.hash[:], b.hash[:]) {
			return ErrEvidenceInvalid
		}
		return nil
	default:
		return ErrEvidenceInvalid
	}
}

// decodedHeader is minimal header info parsed from a canonical block payload.
type decodedHeader struct {
	ok       bool
	height   uint64
	proposer types.Address
	hash     types.Hash
}

// NewVoteEvidenceExport builds double-vote evidence from two signed votes.
func NewVoteEvidenceExport(a, b *types.Vote) *types.Evidence {
	return &types.Evidence{
		Kind: "vote", Offender: a.Voter, Height: a.Height,
		A: signedVoteBytes(a), B: signedVoteBytes(b),
	}
}
