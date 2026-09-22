// Package mempool holds pending transactions and orders them by the
// deterministic packing rule: higher fee first, then sender address,
// then nonce (all ascending/lexicographic except fee).
package mempool

import (
	"errors"
	"sort"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Rejection reasons specific to pool admission on top of state-level ones.
var (
	ErrAlreadyPending = errors.New("mempool: transaction already pending")
	ErrPoolFull       = errors.New("mempool: pool capacity reached")
)

// Mempool is a goroutine-safe pending-transaction store.
type Mempool struct {
	mu       sync.Mutex
	state    *state.State
	feeSink  types.Address
	txs      map[types.Hash]*types.Tx
	capacity int
}

// New creates a mempool validated against the given base state.
func New(base *state.State, feeSink types.Address, capacity int) *Mempool {
	panic("not implemented")
}

// Add validates a transaction against the current base state and admits it.
// It returns state.* / mempool.* distinguishable errors on rejection.
func (m *Mempool) Add(tx *types.Tx) error { panic("not implemented") }

// Pending returns all admitted transactions in deterministic packing order,
// projected against the base state (skipping transactions that have become
// invalid, e.g. after a reorg changed the base state).
func (m *Mempool) Pending() []*types.Tx { panic("not implemented") }

// Pack returns up to maxTxs valid transactions in packing order and drops
// them from the pool. It is used by block producers.
func (m *Mempool) Pack(maxTxs int) []*types.Tx { panic("not implemented") }

// Remove deletes a transaction from the pool.
func (m *Mempool) Remove(h types.Hash) { panic("not implemented") }

// Size returns the number of pending transactions.
func (m *Mempool) Size() int { panic("not implemented") }

// order sorts in place by fee desc, then sender asc, then nonce asc.
func order(txs []*types.Tx) {
	sort.Slice(txs, func(i, j int) bool { panic("not implemented") })
}
