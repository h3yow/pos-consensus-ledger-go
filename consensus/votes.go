package consensus

import (
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
	set      *Set                     // public keys for identity verification
}

// NewVotePool creates an empty vote pool. The validator set is attached later
// (at chain construction) and supplies every voter's PUBLIC key.
func NewVotePool() *VotePool {
	return &VotePool{
		byHeight: make(map[uint64]map[types.Address]*voteRecord),
		order:    make(map[uint64][]*types.Vote),
	}
}

// bindSet attaches the validator set used for signature verification.
func (p *VotePool) bindSet(s *Set) { p.set = s }

// AddVote records a vote. Rules:
//   - the voter MUST be in the validator set and the vote's Ed25519 signature
//     MUST verify against that validator's PUBLIC key (and the key's derived
//     address MUST equal the claimed Voter). Without the validator's private
//     key nobody can forge such a vote;
//   - an identical repeat (same height, same block hash) is a harmless
//     re-broadcast: no error, no evidence;
//   - a second vote for a DIFFERENT block hash at the same height is
//     equivocation. The conflicting vote is retained and evidence is
//     returned so a proposer can include it to slash the offender.
func (p *VotePool) AddVote(v *types.Vote) (*types.Evidence, error) {
	if p.set == nil {
		return nil, ErrBadVote
	}
	pub := p.set.PubKey(v.Voter)
	if pub == nil || !types.VerifyVote(v, pub) {
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
// proves equivocation, using ONLY public information (the validator set):
//   - "vote": two canonical votes by the same voter at one height for two
//     different block hashes, each carrying an Ed25519 signature that verifies
//     against that voter's PUBLIC key;
//   - "propose": two signed canonical blocks at one height by the same
//     proposer with distinct hashes, each carrying an Ed25519 signature that
//     verifies against that proposer's PUBLIC key.
//
// Because signing requires the private key, an attacker cannot fabricate a
// pair that "looks like" an honest validator double-signed: forged, tampered
// or mismatched evidence is rejected with ErrEvidenceInvalid on every node,
// and can never cause an honest validator to be slashed.
func verifyEvidence(s *Set, ev *types.Evidence) error {
	if ev == nil || s == nil {
		return ErrEvidenceInvalid
	}
	pub := s.PubKey(ev.Offender)
	if pub == nil {
		return ErrEvidenceInvalid // offender must be a known validator
	}
	switch ev.Kind {
	case "vote":
		a, err1 := decodeSignedVote(ev.A)
		b, err2 := decodeSignedVote(ev.B)
		if err1 != nil || err2 != nil {
			return ErrEvidenceInvalid
		}
		if !types.VerifyVote(a, pub) || !types.VerifyVote(b, pub) {
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
		a, err1 := decodeSignedBlock(ev.A)
		b, err2 := decodeSignedBlock(ev.B)
		if err1 != nil || err2 != nil {
			return ErrEvidenceInvalid
		}
		if !types.VerifyBlock(a, pub) || !types.VerifyBlock(b, pub) {
			return ErrEvidenceInvalid
		}
		if a.Height != ev.Height || b.Height != ev.Height {
			return ErrEvidenceInvalid
		}
		if a.Proposer != ev.Offender || b.Proposer != ev.Offender {
			return ErrEvidenceInvalid
		}
		if types.HashBlock(a) == types.HashBlock(b) {
			return ErrEvidenceInvalid
		}
		return nil
	default:
		return ErrEvidenceInvalid
	}
}

// NewVoteEvidenceExport builds double-vote evidence from two signed votes.
func NewVoteEvidenceExport(a, b *types.Vote) *types.Evidence {
	return &types.Evidence{
		Kind: "vote", Offender: a.Voter, Height: a.Height,
		A: signedVoteBytes(a), B: signedVoteBytes(b),
	}
}
