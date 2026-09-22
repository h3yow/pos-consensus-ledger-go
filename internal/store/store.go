// Package store is the in-process block DAG storage for one node:
// blocks, votes and per-block resulting states, indexed by hash with
// parent links so competing branches can coexist.
package store

import (
	"errors"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// ErrNotFound is returned for unknown hashes.
var ErrNotFound = errors.New("store: block not found")

// Store is a goroutine-safe in-memory block DAG.
type Store struct {
	mu     sync.RWMutex
	blocks map[types.Hash]*types.Block
	states map[types.Hash]*state.State
}

// New creates an empty store.
func New() *Store { panic("not implemented") }

// PutBlock stores a block (idempotent by hash).
func (s *Store) PutBlock(b *types.Block) { panic("not implemented") }

// GetBlock fetches a block.
func (s *Store) GetBlock(h types.Hash) (*types.Block, error) { panic("not implemented") }

// Has reports whether a block is stored.
func (s *Store) Has(h types.Hash) bool { panic("not implemented") }

// PutState stores the state produced by applying a block.
func (s *Store) PutState(h types.Hash, st *state.State) { panic("not implemented") }

// GetState fetches the state produced by a block.
func (s *Store) GetState(h types.Hash) (*state.State, error) { panic("not implemented") }

// BlocksAt returns all stored blocks at a height (competing proposals).
func (s *Store) BlocksAt(height uint64) []*types.Block { panic("not implemented") }

// Chain returns blocks from genesis up to the given hash (inclusive).
func (s *Store) Chain(h types.Hash) ([]*types.Block, error) { panic("not implemented") }

// VotesStore keeps observed votes keyed by (voter,height) so that
// repeating a vote with a different block hash yields equivocation.
type VotesStore struct {
	mu    sync.RWMutex
	votes map[voteKey]*types.Vote
}

type voteKey struct {
	voter  types.Address
	height uint64
}

// NewVotes creates an empty vote store.
func NewVotes() *VotesStore { panic("not implemented") }

// Add records a vote. Returns the previously seen vote (nil if none).
func (v *VotesStore) Add(vote *types.Vote) *types.Vote { panic("not implemented") }

// Get returns the recorded vote for a voter at a height.
func (v *VotesStore) Get(voter types.Address, height uint64) *types.Vote { panic("not implemented") }

// All returns all recorded votes (for inspection/replay).
func (v *VotesStore) All() []*types.Vote { panic("not implemented") }
