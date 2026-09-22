// Package consensus implements the PoS validator set, weighted round-robin
// proposer schedule, deterministic fork choice, voting/finality and slashing.
package consensus

import (
	"bytes"
	"sort"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// Set is the validator set with weighted stake.
type Set struct {
	validators []*types.Validator
	byAddr     map[types.Address]*types.Validator
	total      uint64
}

// NewSet builds a set; members are kept sorted by Address for determinism.
func NewSet(vs []*types.Validator) *Set {
	cp := make([]*types.Validator, len(vs))
	copy(cp, vs)
	sort.Slice(cp, func(i, j int) bool {
		return bytes.Compare(cp[i].Addr[:], cp[j].Addr[:]) < 0
	})
	s := &Set{
		validators: cp,
		byAddr:     make(map[types.Address]*types.Validator),
	}
	for _, v := range cp {
		s.byAddr[v.Addr] = v
		s.total += v.Stake
	}
	return s
}

// Contains reports whether addr is a validator.
func (s *Set) Contains(addr types.Address) bool {
	_, ok := s.byAddr[addr]
	return ok
}

// Stake returns the stake weight of addr (0 if unknown).
func (s *Set) Stake(addr types.Address) uint64 {
	if v, ok := s.byAddr[addr]; ok {
		return v.Stake
	}
	return 0
}

// TotalStake returns the sum of all stakes.
func (s *Set) TotalStake() uint64 { return s.total }

// Addrs returns validators in deterministic (address-sorted) order.
func (s *Set) Addrs() []types.Address {
	out := make([]types.Address, 0, len(s.validators))
	for _, v := range s.validators {
		out = append(out, v.Addr)
	}
	return out
}

// ProposerAt returns the deterministically scheduled proposer of height h.
//
// Weighted round-robin: imagine an expanded ring in which validator i owns
// Stake_i consecutive slots (validators ordered by address). The ring has
// TotalStake positions; the position for height h is h % TotalStake, and
// whichever validator owns that slot proposes. A validator with 2x stake
// proposes roughly 2x as often, and every node computes the same proposer
// from the same genesis set.
func (s *Set) ProposerAt(h uint64) types.Address {
	if s.total == 0 || len(s.validators) == 0 {
		return types.Address{}
	}
	pos := h % s.total
	var acc uint64
	for _, v := range s.validators {
		if pos < acc+v.Stake {
			return v.Addr
		}
		acc += v.Stake
	}
	return s.validators[len(s.validators)-1].Addr
}
