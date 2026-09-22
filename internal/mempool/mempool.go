// Package mempool holds pending transactions and orders them by the
// deterministic packing rule: higher fee first, then sender address,
// then nonce, then transaction hash (all lexicographic/ascending).
//
// Validity is always projected against the current base state, so after
// a reorg stale transactions are skipped lazily and can re-appear once
// their nonce becomes pending again.
package mempool

import (
	"errors"
	"sort"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Rejection reasons specific to pool admission.
var (
	ErrAlreadyPending   = errors.New("mempool: transaction already pending")
	ErrConflictingNonce = errors.New("mempool: conflicting pending transaction for the same sender and nonce")
	ErrPoolFull         = errors.New("mempool: pool capacity reached")
)

// Mempool is a goroutine-safe pending-transaction store.
type Mempool struct {
	mu       sync.Mutex
	base     *state.State
	txs      map[types.Hash]*types.Tx
	capacity int
}

// New creates a mempool validated against the given base state.
func New(base *state.State, capacity int) *Mempool {
	return &Mempool{
		base:     base,
		txs:      map[types.Hash]*types.Tx{},
		capacity: capacity,
	}
}

// SetBase swaps the state the pool projects against (e.g. after a
// canonical-head change / reorg).
func (m *Mempool) SetBase(base *state.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.base = base
}

// Add validates a transaction against the current base state and admits it.
// It returns distinguishable errors from the state package or this package.
func (m *Mempool) Add(tx *types.Tx) error {
	if tx == nil {
		return errors.New("mempool: nil transaction")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	h := tx.Hash()
	if _, ok := m.txs[h]; ok {
		return ErrAlreadyPending
	}
	if err := m.base.Validate(tx); err != nil {
		return err
	}
	// Same sender/nonce but a different payload: an explicit conflict.
	for _, ex := range m.txs {
		if ex.Sender() == tx.Sender() && ex.Nonce == tx.Nonce {
			return ErrConflictingNonce
		}
	}
	if m.capacity > 0 && len(m.txs) >= m.capacity {
		return ErrPoolFull
	}
	m.txs[h] = tx
	return nil
}

// ordered returns the pending transactions sorted by the packing rule.
func (m *Mempool) ordered() []*types.Tx {
	out := make([]*types.Tx, 0, len(m.txs))
	for _, tx := range m.txs {
		out = append(out, tx)
	}
	order(out)
	return out
}

// Pending returns all currently valid transactions in deterministic
// packing order, projected against a clone of the base state.
func (m *Mempool) Pending() []*types.Tx {
	m.mu.Lock()
	defer m.mu.Unlock()

	proj := m.base.Clone()
	var out []*types.Tx
	for _, tx := range m.ordered() {
		if err := proj.Apply(tx, projFeeSink); err != nil {
			continue // stale / no longer valid against this base
		}
		out = append(out, tx)
	}
	return out
}

// Pack returns up to maxTxs valid transactions in packing order and
// removes them from the pool. maxTxs <= 0 means unlimited.
func (m *Mempool) Pack(maxTxs int) []*types.Tx {
	m.mu.Lock()
	defer m.mu.Unlock()

	proj := m.base.Clone()
	var packed []*types.Tx
	drop := []types.Hash{}
	for _, tx := range m.ordered() {
		if maxTxs > 0 && len(packed) >= maxTxs {
			break
		}
		if err := proj.Apply(tx, projFeeSink); err != nil {
			drop = append(drop, tx.Hash()) // stale against current base
			continue
		}
		packed = append(packed, tx)
		drop = append(drop, tx.Hash())
	}
	for _, h := range drop {
		delete(m.txs, h)
	}
	return packed
}

// Remove deletes a transaction from the pool.
func (m *Mempool) Remove(h types.Hash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.txs, h)
}

// Size returns the number of pending transactions.
func (m *Mempool) Size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.txs)
}

// order sorts in place: fee desc, sender asc, nonce asc, hash asc.
func order(txs []*types.Tx) {
	sort.Slice(txs, func(i, j int) bool {
		a, b := txs[i], txs[j]
		if a.Fee != b.Fee {
			return a.Fee > b.Fee
		}
		if c := a.Sender().Cmp(b.Sender()); c != 0 {
			return c < 0
		}
		if a.Nonce != b.Nonce {
			return a.Nonce < b.Nonce
		}
		return a.Hash().Cmp(b.Hash()) < 0
	})
}

// projFeeSink is only used while simulating packing; fee attribution to
// the real proposer happens during block application.
var projFeeSink = types.Address{}
