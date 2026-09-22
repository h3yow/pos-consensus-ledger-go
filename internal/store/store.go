// Package store is the in-process block DAG storage for one node:
// blocks and their resulting states are indexed by hash with parent
// links so competing branches coexist, plus a vote index keyed by
// (voter, height) for equivocation detection.
package store

import (
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// ErrNotFound is returned for unknown hashes.
const ErrNotFound = storeError("store: block not found")

type storeError string

func (e storeError) Error() string { return string(e) }

// Store is a goroutine-safe in-memory block DAG.
type Store struct {
	mu     sync.RWMutex
	blocks map[types.Hash]*types.Block
	states map[types.Hash]*state.State
}

// New creates an empty store.
func New() *Store {
	return &Store{
		blocks: map[types.Hash]*types.Block{},
		states: map[types.Hash]*state.State{},
	}
}

// PutBlock stores a block (idempotent by hash).
func (s *Store) PutBlock(b *types.Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := b.Hash()
	if _, ok := s.blocks[h]; !ok {
		s.blocks[h] = b
	}
}

// GetBlock fetches a block.
func (s *Store) GetBlock(h types.Hash) (*types.Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blocks[h]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

// Has reports whether a block is stored.
func (s *Store) Has(h types.Hash) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.blocks[h]
	return ok
}

// PutState stores the state produced by applying a block.
func (s *Store) PutState(h types.Hash, st *state.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[h] = st
}

// GetState fetches the state produced by a block.
func (s *Store) GetState(h types.Hash) (*state.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[h]
	if !ok {
		return nil, ErrNotFound
	}
	return st, nil
}

// BlocksAt returns all stored blocks at a height (competing proposals),
// ordered by hash for determinism.
func (s *Store) BlocksAt(height uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*types.Block
	for _, b := range s.blocks {
		if b.Height == height {
			out = append(out, b)
		}
	}
	sortBlocks(out)
	return out
}

// Tips returns blocks that are not referenced as parent by any other
// stored block, ordered by hash.
func (s *Store) Tips() []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()
	child := map[types.Hash]bool{}
	for _, b := range s.blocks {
		child[b.ParentHash] = true
	}
	var out []*types.Block
	for h, b := range s.blocks {
		if !child[h] {
			out = append(out, b)
		}
	}
	sortBlocks(out)
	return out
}

// Chain returns blocks from genesis up to h inclusive, ordered by height.
func (s *Store) Chain(h types.Hash) ([]*types.Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var path []*types.Block
	cur := h
	for {
		b, ok := s.blocks[cur]
		if !ok {
			return nil, ErrNotFound
		}
		path = append(path, b)
		if b.Height == 0 {
			break
		}
		cur = b.ParentHash
	}
	// reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, nil
}

// sortBlocks orders blocks ascending by hash for deterministic iteration.
func sortBlocks(bs []*types.Block) {
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && bs[j-1].Hash().Cmp(bs[j].Hash()) > 0; j-- {
			bs[j-1], bs[j] = bs[j], bs[j-1]
		}
	}
}

// VotesStore keeps observed votes keyed by (voter,height). A second,
// different vote from the same voter at the same height is equivocation.
type VotesStore struct {
	mu    sync.RWMutex
	votes map[voteKey]*types.Vote
}

type voteKey struct {
	voter  types.Address
	height uint64
}

// NewVotes creates an empty vote store.
func NewVotes() *VotesStore {
	return &VotesStore{votes: map[voteKey]*types.Vote{}}
}

// Add records a vote and returns the previously seen one (nil if none).
func (v *VotesStore) Add(vote *types.Vote) *types.Vote {
	v.mu.Lock()
	defer v.mu.Unlock()
	k := voteKey{vote.Voter, vote.Height}
	prior := v.votes[k]
	if prior == nil {
		v.votes[k] = vote
	}
	return prior
}

// Get returns the recorded vote for a voter at a height.
func (v *VotesStore) Get(voter types.Address, height uint64) *types.Vote {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.votes[voteKey{voter, height}]
}

// All returns all recorded votes ordered by (height, voter) for stable output.
func (v *VotesStore) All() []*types.Vote {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]*types.Vote, 0, len(v.votes))
	for _, vote := range v.votes {
		out = append(out, vote)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if a.Height < b.Height || (a.Height == b.Height && a.Voter.Cmp(b.Voter) < 0) {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
