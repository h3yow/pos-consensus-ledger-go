// Package pool is the in-memory transaction mempool with a hard capacity and a
// single, documented eviction policy.
//
// Invariant (the headline rule): NO admission or eviction may ever create a
// NEW nonce gap for a sender — the pool must never keep a sender's higher
// waiting nonce while dropping a smaller nonce that sender still needs first.
//
// Capacity / eviction (predictable under sustained pressure):
//   - the pool holds at most Cap pending transactions;
//   - when full, room is made by evicting the globally LOWEST-priority
//     occupant, where priority is: higher fee wins, and at equal fee the
//     EARLIER arrival wins (so ties evict the newest); a hash tie-break keeps
//     the comparison total;
//   - an occupant is evictable only when it is the highest nonce its sender
//     would have AFTER the incoming transaction joins the pool. That is,
//     per-sender maximum nonces are computed over the CURRENT occupants PLUS
//     the incoming tx. Therefore, when the newcomer continues that same
//     sender's run (or jumps ahead of it), every existing occupant of that
//     sender sits below the new maximum and is protected; the cap=1 case
//     (nonce0 present, nonce1 submitted) is no exception;
//   - the incoming transaction is always part of the comparison. If it is the
//     lowest priority it is itself rejected (nothing is evicted);
//   - if at least one eligible occupant exists but the newcomer loses to all
//     of them on fee/arrival priority, the newcomer is rejected with
//     ErrPoolFull; if NO occupant is eligible, admitting the newcomer could
//     only be paid for by dropping a required smaller nonce, so it is
//     rejected wholesale with ErrWouldGapNonce. In both rejection cases the
//     pool contents and capacity are left exactly as they were.
//
// Admission distinguishes: bad signature, insufficient balance, nonce too low,
// nonce gap (too high), overflow, pure duplicate, same-nonce conflict,
// outbid-at-capacity (ErrPoolFull) and would-gap-at-capacity
// (ErrWouldGapNonce).
//
// After a new block the pool Rebases onto the new state: committed txs drop
// out and the survivors keep their original arrival sequence. Packing order is
// a pure function of the survivor set (fee desc, nonce asc, hash asc), so all
// nodes that hold the same pending transactions pack them identically.
package pool

import (
	"bytes"
	"sort"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TxEntry pairs a transaction with its arrival sequence number.
type TxEntry struct {
	Tx  *types.Tx
	Seq uint64
}

// Pool stores pending transactions keyed by hash. All operations are
// goroutine-safe: gossip delivery may submit txs concurrently with block
// rebases, so a single RWMutex guards the pending set, slot index and seq.
type Pool struct {
	mu      sync.RWMutex
	st      *state.State
	cap     int // <= 0 means unlimited
	pending map[types.Hash]*TxEntry
	slot    map[slotKey]types.Hash // one tx per (sender,nonce)
	seq     uint64
}

type slotKey struct {
	from  types.Address
	nonce uint64
}

// New creates a pool bound to a state used for admission checks. capacity is
// the hard maximum number of pending transactions (<= 0 disables the limit).
func New(st *state.State, capacity int) *Pool {
	return &Pool{
		st:      st,
		cap:     capacity,
		pending: make(map[types.Hash]*TxEntry),
		slot:    make(map[slotKey]types.Hash),
	}
}

// Add validates a transaction and admits it, applying the eviction policy when
// the pool is full. The error sentinel identifies the exact rejection reason.
func (p *Pool) Add(t *types.Tx) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addLocked(t)
}

func (p *Pool) addLocked(t *types.Tx) error {
	h := types.HashBytes(types.CanonicalTx(t))
	if _, ok := p.pending[h]; ok {
		return ErrAlreadyKnown
	}
	// Content-independent validity first, so a malformed tx never occupies a
	// slot or triggers eviction of a valid one.
	if !types.VerifyTx(t) {
		return state.ErrBadSignature
	}
	if t.Value > (^uint64(0))-t.Fee {
		return state.ErrOverflows
	}
	cur := p.st.GetNonce(t.From)
	if t.Nonce < cur {
		return state.ErrNonceTooLow
	}
	if p.st.GetBalance(t.From) < t.Value+t.Fee {
		return state.ErrInsufficient
	}
	// One transaction per (sender,nonce): a different body for an occupied
	// slot is a conflict, rejected (never silently replacing the original).
	k := slotKey{t.From, t.Nonce}
	if _, occupied := p.slot[k]; occupied {
		return ErrSameNonceConflict
	}

	// Capacity pressure: choose the lowest-priority member among current
	// occupants PLUS the incoming tx. Eligibility is computed against that
	// same candidate set so admitting the newcomer can never leave one of its
	// sender's smaller required nonces behind.
	if p.cap > 0 && len(p.pending) >= p.cap {
		victim, occupantEligible := p.chooseEvictionVictim(t, h)
		if victim == nil || victim.hash == h {
			// The incoming transaction loses. WHY it loses matters to the
			// caller: ErrPoolFull when an eligible occupant exists but
			// outranks it; ErrWouldGapNonce when every occupant is protected
			// (admission would force a gap). Either way nothing is touched.
			if occupantEligible {
				return ErrPoolFull
			}
			return ErrWouldGapNonce
		}
		p.evict(victim.hash)
	}

	p.seq++
	p.pending[h] = &TxEntry{Tx: t, Seq: p.seq}
	p.slot[k] = h
	return nil
}

type victim struct {
	hash types.Hash
	seq  uint64
	fee  uint64
}

// lowerPriority reports whether a should be evicted before b:
// lower fee first; equal fee -> LATER arrival first (earlier is protected);
// final tie -> larger hash first (total, deterministic order).
func lowerPriority(a, b victim) bool {
	if a.fee != b.fee {
		return a.fee < b.fee
	}
	if a.seq != b.seq {
		return a.seq > b.seq
	}
	return bytes.Compare(a.hash[:], b.hash[:]) > 0
}

// chooseEvictionVictim returns the lowest-priority ELIGIBLE candidate
// considering the incoming tx (seq = next, hash = h), plus whether at least
// one CURRENT occupant is eligible at all.
//
// The maximum waiting nonce of each sender is computed over the candidate set
// CURRENT OCCUPANTS + INCOMING TX. An occupant is eligible exactly when its
// nonce equals its sender's maximum in THAT set. Consequences:
//   - if the incoming tx is from sender s with nonce N, then N is s's maximum
//     in the candidate set (its slot is unoccupied), so every existing tx of s
//     sits strictly below N and is protected — the newcomer cannot buy entry
//     by orphaning s's own required prefix;
//   - a different sender's tail stays evictable (evicting it leaves that
//     sender with no higher kept nonce), which is exactly the room a
//     non-continuing newcomer may take.
func (p *Pool) chooseEvictionVictim(in *types.Tx, inHash types.Hash) (*victim, bool) {
	// Highest waiting nonce per sender in the candidate set, INCLUDING the
	// incoming tx — this is the fix: maxima over current occupants alone let a
	// same-sender newcomer demote the old tail into a protected-but-evicted
	// middle position.
	maxNonce := make(map[types.Address]uint64)
	for _, e := range p.pending {
		if n, ok := maxNonce[e.Tx.From]; !ok || e.Tx.Nonce > n {
			maxNonce[e.Tx.From] = e.Tx.Nonce
		}
	}
	if n, ok := maxNonce[in.From]; !ok || in.Nonce > n {
		maxNonce[in.From] = in.Nonce
	}

	occupantEligible := false
	var best *victim
	for hh, e := range p.pending {
		if e.Tx.Nonce != maxNonce[e.Tx.From] {
			continue // protected: removing it would gap the sender's sequence
		}
		occupantEligible = true
		cand := victim{hash: hh, seq: e.Seq, fee: e.Tx.Fee}
		if best == nil || lowerPriority(cand, *best) {
			c := cand
			best = &c
		}
	}
	// The incoming tx is always a tail candidate (its slot is unoccupied and
	// its nonce is its sender's maximum in the candidate set).
	inCand := victim{hash: inHash, seq: p.seq + 1, fee: in.Fee}
	if best == nil || lowerPriority(inCand, *best) {
		return &inCand, occupantEligible
	}
	return best, occupantEligible
}

func (p *Pool) evict(h types.Hash) {
	e, ok := p.pending[h]
	if !ok {
		return
	}
	delete(p.pending, h)
	delete(p.slot, slotKey{e.Tx.From, e.Tx.Nonce})
}

// Ordered returns transactions ready for a block, sorted by:
//  1. fee descending (higher fee first),
//  2. nonce ascending (a sender's sequence stays in order),
//  3. transaction hash ascending (content tie-break => identical result on
//     every node regardless of local arrival order).
//
// Gapped / no longer executable transactions are skipped, so the returned
// list is executable in order against st.
func (p *Pool) Ordered(limit int) []*types.Tx {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entries := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Tx.Fee != b.Tx.Fee {
			return a.Tx.Fee > b.Tx.Fee
		}
		if a.Tx.Nonce != b.Tx.Nonce {
			return a.Tx.Nonce < b.Tx.Nonce
		}
		ha := types.HashBytes(types.CanonicalTx(a.Tx))
		hb := types.HashBytes(types.CanonicalTx(b.Tx))
		return bytes.Compare(ha[:], hb[:]) < 0
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
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pending)
}

// Has reports whether the exact transaction (by canonical hash) is pending.
func (p *Pool) Has(t *types.Tx) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.pending[types.HashBytes(types.CanonicalTx(t))]
	return ok
}

// Pending returns a snapshot of the pending entries in arrival order.
func (p *Pool) Pending() []*TxEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Rebase swaps the pool's underlying state (e.g. after a new head block) and
// re-admits every pending transaction in arrival order, silently dropping
// transactions that are now invalid (committed, replayed or unfunded). Because
// re-admission follows the same deterministic rules on every node and the
// survivors keep their original arrival sequences, nodes converge on the same
// survivor set and packing order.
func (p *Pool) Rebase(st *state.State) {
	p.mu.Lock()
	defer p.mu.Unlock()

	saved := make([]*TxEntry, 0, len(p.pending))
	for _, e := range p.pending {
		saved = append(saved, e)
	}
	sort.Slice(saved, func(i, j int) bool { return saved[i].Seq < saved[j].Seq })

	p.st = st
	p.pending = make(map[types.Hash]*TxEntry)
	p.slot = make(map[slotKey]types.Hash)
	nextSeq := uint64(0)
	for _, e := range saved {
		t := e.Tx
		// Mirror addLocked's validation without eviction: re-admit survivors.
		h := types.HashBytes(types.CanonicalTx(t))
		if !types.VerifyTx(t) {
			continue
		}
		if t.Value > (^uint64(0))-t.Fee {
			continue
		}
		if t.Nonce < st.GetNonce(t.From) {
			continue // committed in the new block
		}
		if st.GetBalance(t.From) < t.Value+t.Fee {
			continue
		}
		k := slotKey{t.From, t.Nonce}
		if _, dup := p.slot[k]; dup {
			continue
		}
		nextSeq++
		p.pending[h] = &TxEntry{Tx: t, Seq: nextSeq}
		p.slot[k] = h
	}
	p.seq = nextSeq
}
