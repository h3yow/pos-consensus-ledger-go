// Package pool is the in-memory transaction mempool.
package pool

import (
	"sort"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TxEntry pairs a transaction with its insertion sequence number.
type TxEntry struct {
	Tx  *types.Tx
	Seq uint64
}

// Pool stores pending transactions keyed by hash.
type Pool struct {
	st      *state.State
	pending map[types.Hash]*TxEntry
	seq     uint64
}

// New creates a pool bound to a state used for admission checks.
func New(st *state.State) *Pool {
	return &Pool{st: st, pending: make(map[types.Hash]*TxEntry)}
}

// Add validates a transaction against the current state and admits it.
// A transaction is admissible when its nonce is at least the account's
// committed nonce and its balance can cover value+fee (queueing of future
// nonces is allowed; ordering is enforced at block packing).
// Duplicate hashes return ErrAlreadyKnown; other failures pass through the
// distinguishable state errors.
func (p *Pool) Add(t *types.Tx) error {
	h := types.HashBytes(types.CanonicalTx(t))
	if _, ok := p.pending[h]; ok {
		return ErrAlreadyKnown
	}
	if !types.VerifyTx(t) {
		return state.ErrBadSignature
	}
	cur := p.st.GetNonce(t.From)
	if t.Nonce < cur {
		return state.ErrNonceTooLow
	}
	// Balance must cover value+fee; gaps/overflow are rejected up front too.
	if t.Value > (^uint64(0))-t.Fee {
		return state.ErrOverflows
	}
	if p.st.GetBalance(t.From) < t.Value+t.Fee {
		return state.ErrInsufficient
	}
	p.seq++
	p.pending[h] = &TxEntry{Tx: t, Seq: p.seq}
	return nil
}

// Ordered returns transactions ready for a block, sorted by:
//  1. fee descending (higher fee first),
//  2. nonce ascending (a sender's sequence stays in order),
//  3. arrival sequence ascending (total tie-break => deterministic).
//
// Gapped / no longer executable transactions are skipped, so the returned
// list is executable in order against st.
func (p *Pool) Ordered(limit int) []*types.Tx {
	entries := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Tx.Fee != b.Tx.Fee {
			return a.Tx.Fee > b.Tx.Fee
		}
		if a.Tx.Nonce != b.Tx.Nonce {
			return a.Tx.Nonce < b.Tx.Nonce
		}
		return a.Seq < b.Seq
	})

	out := make([]*types.Tx, 0, len(entries))
	// Track per-sender expected nonces so that out-of-order/gapped txs from the
	// same sender never block later executable ones and never double-spend.
	expect := make(map[types.Address]uint64)
	for _, e := range entries {
		t := e.Tx
		want, seen := expect[t.From]
		if !seen {
			want = p.st.GetNonce(t.From)
		}
		if t.Nonce != want {
			continue // gap or replay against current packing state
		}
		if p.st.GetBalance(t.From) < t.Value+t.Fee {
			continue // no longer affordable given earlier picks
		}
		out = append(out, t)
		expect[t.From] = want + 1
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Size returns the number of pending transactions.
func (p *Pool) Size() int { return len(p.pending) }

// Rebase swaps the pool's underlying state (e.g. after a new head block) and
// re-admits every pending transaction, silently dropping transactions that
// are now invalid (committed, replayed or unfunded). Pending order/seqs are
// preserved so packing stays deterministic.
func (p *Pool) Rebase(st *state.State) {
	saved := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		saved = append(saved, e)
	}
	sort.Slice(saved, func(i, j int) bool { return saved[i].Seq < saved[j].Seq })

	p.st = st
	p.pending = make(map[types.Hash]*TxEntry)
	for _, e := range saved {
		if err := p.Add(e.Tx); err != nil {
			continue
		}
	}
}
