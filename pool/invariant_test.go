package pool_test

import (
	"errors"
	"math/rand"
	"sort"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// nonceSet groups pending nonces per sender from a pool snapshot.
type nonceSet map[types.Address]map[uint64]bool

func snapshotNonces(p *pool.Pool) nonceSet {
	ns := nonceSet{}
	for _, e := range p.Pending() {
		if ns[e.Tx.From] == nil {
			ns[e.Tx.From] = map[uint64]bool{}
		}
		ns[e.Tx.From][e.Tx.Nonce] = true
	}
	return ns
}

// assertNoEvictionGap verifies that any slot removed during the last Add was a
// per-sender tail at the moment of removal: after removal its sender keeps no
// strictly larger pending nonce. That is exactly "eviction never creates a new
// gap". Pre-existing gaps (future nonces admitted before smaller ones arrived)
// are legal and not flagged.
func assertNoEvictionGap(t *testing.T, before, after nonceSet, cap int, label string) {
	t.Helper()
	for s, set := range before {
		for n := range set {
			if after[s] != nil && after[s][n] {
				continue // still pending
			}
			if after[s] == nil {
				continue // whole sender gone: no higher nonce kept
			}
			for m := range after[s] {
				if m > n {
					t.Fatalf("%s: eviction of nonce %d from sender %x left higher pending nonce %d (NEW gap)",
						label, n, s[:8], m)
				}
			}
		}
	}
}

// TestEvictionInvariantRandomized runs a fixed-seed, reproducible stream of
// submissions against a small capped pool with multiple senders and asserts
// after EVERY accepted add that the eviction removed only eligible tails, and
// after EVERY add (accepted or not) that capacity and slot uniqueness hold.
func TestEvictionInvariantRandomized(t *testing.T) {
	st := state.New()
	const senders = 4
	addrs := make([]types.Address, senders)
	for i := range addrs {
		addrs[i] = mkAddr(byte(30 + i))
		st.SetBalance(addrs[i], 1_000_000_000)
	}
	dest := mkAddr(99)
	const cap = 5
	p := pool.New(st, cap)
	rng := rand.New(rand.NewSource(20261003))

	for iter := 0; iter < 3000; iter++ {
		s := addrs[rng.Intn(senders)]
		// Nonces stay small so contiguous/future patterns both occur often.
		nonce := uint64(rng.Intn(6))
		fee := uint64(rng.Intn(6)) // 0..5, ties are frequent on purpose
		in := tx(s, dest, nonce, 1, fee)

		before := snapshotNonces(p)
		sizeBefore := p.Size()
		err := p.Add(in)
		after := snapshotNonces(p)

		if p.Size() > cap {
			t.Fatalf("iter %d: capacity breached: %d > %d", iter, p.Size(), cap)
		}
		if err == nil {
			if p.Size() != sizeBefore && p.Size() != sizeBefore+1 {
				t.Fatalf("iter %d: accepted add changed size %d -> %d unexpectedly", iter, sizeBefore, p.Size())
			}
			if !after[s][nonce] {
				t.Fatalf("iter %d: accepted tx nonce=%d not pending", iter, nonce)
			}
			assertNoEvictionGap(t, before, after, cap, "iter")
		} else {
			if p.Size() != sizeBefore {
				t.Fatalf("iter %d: rejected add changed size %d -> %d (%v)", iter, sizeBefore, p.Size(), err)
			}
			assertNoEvictionGap(t, before, after, cap, "iter")
		}
	}

	// Report the final shape so -v shows the pressure actually moved txs.
	var rows []string
	for s, set := range snapshotNonces(p) {
		var ns []uint64
		for n := range set {
			ns = append(ns, n)
		}
		sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
		rows = append(rows, shortAddr(s)+":"+joinU64(ns))
	}
	sort.Strings(rows)
	t.Logf("decision: 3000 randomized adds, cap=%d never breached, every eviction tail-only; final nonces %v",
		cap, rows)
}

func shortAddr(a types.Address) string {
	return string([]byte{'s', a[19]/10 + '0', a[19]%10 + '0'})
}

func joinU64(ns []uint64) string {
	b := []byte{'{'}
	for i, n := range ns {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, byte('0'+n))
	}
	b = append(b, '}')
	return string(b)
}

// TestScriptedNoGapSequence replays a hand-designed pressure stream with two
// phases. Phase A: another sender's evictable tail legitimately buys room for a
// sender's next nonce (no gap created). Phase B: once the whole pool is one
// sender's contiguous run, the same sender's next nonce must be refused even at
// a huge fee — accepting it would force dropping its own required prefix.
// Every step's before/after sets are checked for the tail-only invariant.
func TestScriptedNoGapSequence(t *testing.T) {
	st := state.New()
	a, b, c := mkAddr(1), mkAddr(2), mkAddr(3)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)
	st.SetBalance(c, 1_000_000)
	p := pool.New(st, 3)

	type step struct {
		from      types.Address
		nonce     uint64
		fee       uint64
		wantError bool
		wantErrIs error
		label     string
	}
	steps := []step{
		// Phase A: cross-sender room is legitimate.
		{a, 0, 10, false, nil, "a0"},
		{a, 1, 10, false, nil, "a1 (a run {0,1})"},
		{b, 0, 9, false, nil, "b0 (fills cap)"},
		{a, 2, 20, false, nil, "a2 outbids b0; evicting b0 gaps nothing (b keeps no higher nonce)"},
		// Now {a0,a1,a2}: one sender's contiguous run fills the whole pool.
		// Phase B: same-sender continuation must be refused.
		{a, 3, 500, true, pool.ErrWouldGapNonce, "a3 fee500: no other-sender tail left; accepting would drop a required prefix"},
		{c, 0, 1, true, pool.ErrPoolFull, "c0 fee1: eligible tails exist but it cannot outbid them"},
	}
	for i, s := range steps {
		in := tx(s.from, a, s.nonce, 1, s.fee)
		before := snapshotNonces(p)
		err := p.Add(in)
		after := snapshotNonces(p)
		if s.wantError {
			if err == nil {
				t.Fatalf("step %d (%s): want rejection, got accepted; before=%v after=%v",
					i, s.label, before, after)
			}
			if s.wantErrIs != nil && !errors.Is(err, s.wantErrIs) {
				t.Fatalf("step %d (%s): want %v, got %v", i, s.label, s.wantErrIs, err)
			}
		} else if err != nil {
			t.Fatalf("step %d (%s): %v", i, s.label, err)
		}
		if p.Size() > 3 {
			t.Fatalf("step %d: cap breached size=%d", i, p.Size())
		}
		assertNoEvictionGap(t, before, after, 3, s.label)
		t.Logf("step %d input=%s fee=%d -> accepted=%v err=%v size=%d", i, s.label, s.fee, err == nil, err, p.Size())
	}
	if !p.Has(tx(a, a, 0, 1, 10)) || !p.Has(tx(a, a, 1, 1, 10)) || !p.Has(tx(a, a, 2, 1, 20)) {
		t.Fatal("a's run {a0,a1,a2} must be intact at the end")
	}
	t.Logf("decision: scripted stream complete: cross-sender room used legally, same-sender continuation refused, no new gap, cap=3 respected")
}
