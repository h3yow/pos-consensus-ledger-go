package consensus

import (
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// voteRecord keeps a stored vote and its evidence payload.
type voteRecord struct {
	vote *types.Vote
	raw  []byte // canonical encoding + signature, for evidence construction
}

// VotePool keeps votes per height and detects equivocation. Every vote is
// authenticated against the validator set's public keys before it is stored.
type VotePool struct {
	set *Set
	// byHeight maps height -> voter -> their recorded vote.
	byHeight map[uint64]map[types.Address]*voteRecord
	order    map[uint64][]*types.Vote // insertion order, deterministic replay
}

// NewVotePool creates an empty vote pool bound to the validator set whose
// public keys authenticate every incoming vote.
func NewVotePool(set *Set) *VotePool {
	return &VotePool{
		set:      set,
		byHeight: make(map[uint64]map[types.Address]*voteRecord),
		order:    make(map[uint64][]*types.Vote),
	}
}

// NewVotePoolExport exposes the constructor for external tests.
func NewVotePoolExport(set *Set) *VotePool { return NewVotePool(set) }

// AddVote records a vote. Rules:
//   - the signature must verify against the voter's genesis public key,
//     otherwise the vote is rejected (unknown voter is the same failure:
//     no public key can authenticate it);
//   - an identical repeat (same height, same block hash) is a harmless
//     re-broadcast: no error, no evidence;
//   - a second vote for a DIFFERENT block hash at the same height is
//     equivocation. The conflicting vote is retained and evidence is
//     returned so a proposer can include it to slash the offender.
func (p *VotePool) AddVote(v *types.Vote) (*types.Evidence, error) {
	if !p.set.VerifyVote(v) {
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

// NewVoteEvidenceExport builds double-vote evidence from two signed votes.
func NewVoteEvidenceExport(a, b *types.Vote) *types.Evidence {
	return &types.Evidence{
		Kind: "vote", Offender: a.Voter, Height: a.Height,
		A: signedVoteBytes(a), B: signedVoteBytes(b),
	}
}
