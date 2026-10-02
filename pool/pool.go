// Package pool is the bounded, deterministic in-memory transaction mempool.
package pool

import (
	"sort"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// DefaultCapacity is the pool cap when New is used without an explicit one.
const DefaultCapacity = 10_000

// TxEntry pairs a transaction with its insertion sequence number.
type TxEntry struct {
	Tx  *types.Tx
	Seq uint64
}

// Pool stores pending transactions. It is bounded: once Capacity slots are
// occupied, admission evicts strictly lower-priority entries by an explicit
// rule (see Add).
type Pool struct {
	st       *state.State
	capacity int
	pending  map[types.Hash]*TxEntry
	bySlot   map[types.Address]map[uint64]types.Hash // sender -> nonce -> tx hash
	seq      uint64
}

// New creates a pool bound to a state used for admission checks, with the
// default capacity.
func New(st *state.State) *Pool {
	return NewWithCap(st, DefaultCapacity)
}

// NewWithCap creates a pool with an explicit capacity (>0 enforced).
func NewWithCap(st *state.State, capacity int) *Pool {
	if capacity < 1 {
		capacity = 1
	}
	return &Pool{
		st:       st,
		capacity: capacity,
		pending:  make(map[types.Hash]*TxEntry),
		bySlot:   make(map[types.Address]map[uint64]types.Hash),
	}
}

// Capacity returns the maximum number of pending transactions.
func (p *Pool) Capacity() int { return p.capacity }

// Add validates a transaction and admits it under the pool's admission rule.
//
// Basic admission (checked first, the rejection reasons stay mutually
// distinguishable):
//   - bad signature              -> state.ErrBadSignature
//   - value+fee overflow         -> state.ErrOverflows
//   - balance < value+fee        -> state.ErrInsufficient
//   - nonce below committed      -> state.ErrNonceTooLow
//   - byte-identical duplicate   -> ErrAlreadyKnown
//   - same sender+nonce occupied
//     by a DIFFERENT transaction -> ErrSameNonceConflict
//
// Capacity rule: when the pool is full the newcomer is admitted only if it
// outranks the weakest incumbent by the eviction ordering:
//
//	fee ascending (lowest fee is evicted first),
//	arrival seq descending (same fee: the newest is evicted first).
//
// Evicting one transaction may never leave a sender's queued sequence with a
// hole, so evicting nonce k of sender S cascades: every still-queued
// nonce >= k of S (a suffix of S's sorted queued nonces) is evicted too.
// If the cascade reaches the newcomer itself, nothing is displaced and the
// newcomer is rejected with ErrPoolFull — an incumbent suffix is never
// broken on its behalf.
func (p *Pool) Add(t *types.Tx) error {
	h := types.HashBytes(types.CanonicalTx(t))
	if err := p.validate(t, h); err != nil {
		return err
	}

	// Tentatively admit, assign the arrival sequence, then enforce capacity.
	p.seq++
	return p.admit(&TxEntry{Tx: t, Seq: p.seq}, h)
}

// validate runs the order-sensitive admission checks against state and the
// currently occupied (sender,nonce) slots.
func (p *Pool) validate(t *types.Tx, h types.Hash) error {
	if _, ok := p.pending[h]; ok {
		return ErrAlreadyKnown
	}
	if !types.VerifyTx(t) {
		return state.ErrBadSignature
	}
	if t.Value > (^uint64(0))-t.Fee {
		return state.ErrOverflows
	}
	if p.st.GetBalance(t.From) < t.Value+t.Fee {
		return state.ErrInsufficient
	}
	if t.Nonce < p.st.GetNonce(t.From) {
		return state.ErrNonceTooLow
	}
	if slots := p.bySlot[t.From]; slots != nil {
		if _, occupied := slots[t.Nonce]; occupied {
			return ErrSameNonceConflict
		}
	}
	return nil
}

// admit inserts an already-validated entry and enforces the capacity rule,
// rolling every displacement back (and returning ErrPoolFull) if the newcomer
// itself is caught by the eviction cascade.
func (p *Pool) admit(entry *TxEntry, h types.Hash) error {
	p.put(entry, h)

	var evicted []*TxEntry
	newcomerEvicted := false
	for len(p.pending) > p.capacity {
		victim := p.weakest()
		cascade := p.suffixOf(victim.Tx.From, victim.Tx.Nonce)
		for _, e := range cascade {
			eh := types.HashBytes(types.CanonicalTx(e.Tx))
			p.remove(eh)
			if eh == h {
				newcomerEvicted = true
				continue // never restore the rejected newcomer
			}
			evicted = append(evicted, e)
		}
		if newcomerEvicted {
			break // cascade necessarily removed the tentative newcomer
		}
	}

	if !newcomerEvicted {
		if _, admitted := p.pending[h]; admitted {
			return nil // newcomer survived: eviction stands
		}
		newcomerEvicted = true
		p.remove(h)
	}

	// Rejected: restore every displaced incumbent so the pool is unchanged.
	for _, e := range evicted {
		p.put(e, types.HashBytes(types.CanonicalTx(e.Tx)))
	}
	return ErrPoolFull
}

// put indexes an entry by hash and by (sender,nonce).
func (p *Pool) put(e *TxEntry, h types.Hash) {
	p.pending[h] = e
	slots := p.bySlot[e.Tx.From]
	if slots == nil {
		slots = make(map[uint64]types.Hash)
		p.bySlot[e.Tx.From] = slots
	}
	slots[e.Tx.Nonce] = h
}

// remove drops one entry from both indexes.
func (p *Pool) remove(h types.Hash) {
	e, ok := p.pending[h]
	if !ok {
		return
	}
	delete(p.pending, h)
	if slots := p.bySlot[e.Tx.From]; slots != nil {
		if delete(slots, e.Tx.Nonce); len(slots) == 0 {
			delete(p.bySlot, e.Tx.From)
		}
	}
}

// weakest returns the entry with the lowest retention priority: smallest
// fee, breaking ties by the newest arrival sequence.
func (p *Pool) weakest() *TxEntry {
	var w *TxEntry
	for _, e := range p.pending {
		if w == nil ||
			e.Tx.Fee < w.Tx.Fee ||
			(e.Tx.Fee == w.Tx.Fee && e.Seq > w.Seq) {
			w = e
		}
	}
	return w
}

// suffixOf returns all queued entries of sender whose nonce >= start, in
// ascending nonce order. Eviction acts only on such suffixes, so the
// surviving queued nonces of every sender stay contiguous from the chain
// nonce (no holes introduced by eviction).
func (p *Pool) suffixOf(sender types.Address, start uint64) []*TxEntry {
	slots := p.bySlot[sender]
	if slots == nil {
		return nil
	}
	nonces := make([]uint64, 0, len(slots))
	for n := range slots {
		if n >= start {
			nonces = append(nonces, n)
		}
	}
	sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })
	out := make([]*TxEntry, 0, len(nonces))
	for _, n := range nonces {
		out = append(out, p.pending[slots[n]])
	}
	return out
}

// Ordered returns transactions ready for a block in the deterministic
// packing order:
//  1. fee descending (higher fee first),
//  2. nonce ascending (a sender's sequence stays in order),
//  3. arrival sequence ascending (same fee: earlier arrival first),
//  4. tx hash ascending (final content-defined tie-break, identical on every
//     node),
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
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		ha := types.HashBytes(types.CanonicalTx(a.Tx))
		hb := types.HashBytes(types.CanonicalTx(b.Tx))
		return string(ha[:]) < string(hb[:])
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
// re-admits every pending transaction in arrival order, silently dropping
// transactions that are now invalid (committed, replayed or unfunded).
// Arrival sequences are preserved, so given the same retained set every node
// packs blocks in the same order.
func (p *Pool) Rebase(st *state.State) {
	saved := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		saved = append(saved, e)
	}
	sort.Slice(saved, func(i, j int) bool { return saved[i].Seq < saved[j].Seq })

	p.st = st
	p.pending = make(map[types.Hash]*TxEntry)
	p.bySlot = make(map[types.Address]map[uint64]types.Hash)
	for _, e := range saved {
		h := types.HashBytes(types.CanonicalTx(e.Tx))
		// Re-validate against the new head state but KEEP the original arrival
		// sequence, so identical retained sets on different nodes also keep the
		// identical same-fee tie-break and packing order.
		if err := p.validate(e.Tx, h); err != nil {
			continue
		}
		p.put(e, h)
	}
}
