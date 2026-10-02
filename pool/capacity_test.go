package pool_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func mkAddr(i byte) types.Address { var a types.Address; a[19] = i; return a }

func tx(from, to types.Address, nonce, value, fee uint64) *types.Tx {
	return types.SignTx(&types.Tx{From: from, To: to, Nonce: nonce, Value: value, Fee: fee})
}

// TestPoolCapacityEvictionOrder fills a capped pool and asserts the exact
// eviction policy: lowest fee first (earlier arrival protected at equal fee),
// and eviction never opens a nonce gap for a sender (only per-sender tails are
// evictable, so a cheap middle tx survives even when it is globally cheapest).
func TestPoolCapacityEvictionOrder(t *testing.T) {
	st := state.New()
	a, b, c := mkAddr(1), mkAddr(2), mkAddr(3)
	for _, x := range []types.Address{a, b, c} {
		st.SetBalance(x, 100000)
	}
	const cap = 3
	p := pool.New(st, cap)

	must := func(tx *types.Tx) {
		t.Helper()
		if err := p.Add(tx); err != nil {
			t.Fatalf("add nonce=%d fee=%d: %v", tx.Nonce, tx.Fee, err)
		}
	}

	// Fill: a0 a1 a2, all fee 10, a contiguous run for sender a.
	a0 := tx(a, b, 0, 1, 10)
	a1 := tx(a, b, 1, 1, 10)
	a2 := tx(a, b, 2, 1, 10)
	must(a0)
	must(a1)
	must(a2)

	// Full. A low-fee newcomer must NOT displace a higher-fee occupant.
	low := tx(b, a, 0, 1, 1)
	if err := p.Add(low); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("low-fee newcomer: want ErrPoolFull, got %v", err)
	}
	if p.Size() != cap {
		t.Fatalf("pool size = %d, want %d (rejected add must not evict)", p.Size(), cap)
	}
	t.Logf("decision: pool full, fee=1 newcomer rejected (lowest fee stays out)")

	// A higher-fee newcomer evicts the lowest-priority ELIGIBLE tail: a2.
	hi := tx(b, a, 0, 1, 20)
	if err := p.Add(hi); err != nil {
		t.Fatalf("high-fee newcomer: %v", err)
	}
	if p.Has(a2) {
		t.Fatal("a2 (fee10) should have been evicted by fee20 newcomer")
	}
	if !p.Has(a0) || !p.Has(a1) {
		t.Fatal("a0/a1 must survive: sender a's sequence must stay contiguous from nonce 0")
	}
	t.Logf("decision: fee=20 newcomer evicted a's tail a2; a nonces pending = {0,1} (no gap)")

	// Cheap-middle protection: refill a2 with fee 100, then make a0/a2 pricey
	// and a1 the globally cheapest tx (fee1). a1 is a MIDDLE entry and must be
	// protected even though a2 (tail, fee100) is what gets compared/evicted.
	p2 := pool.New(st, cap)
	ca0 := tx(a, b, 0, 1, 100)
	ca1 := tx(a, b, 1, 1, 1)
	ca2 := tx(a, b, 2, 1, 100)
	must2 := func(x *types.Tx) {
		t.Helper()
		if err := p2.Add(x); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	must2(ca0)
	must2(ca1)
	must2(ca2)
	rich := tx(c, a, 0, 1, 200)
	if err := p2.Add(rich); err != nil {
		t.Fatalf("rich newcomer: %v", err)
	}
	if !p2.Has(ca1) {
		t.Fatal("cheap MIDDLE tx a1 must be protected: removing it would gap nonce 1")
	}
	if p2.Has(ca2) {
		t.Fatal("a2 (eligible tail) should have been evicted instead of the cheap middle")
	}
	t.Logf("decision: globally cheapest a1(fee1) protected as middle; tail a2 evicted; no nonce gap")
}

// TestPoolSameNonceConflict checks the three distinct "already there" outcomes:
// pure duplicate, same-nonce different-content conflict, and packed order
// continuity after the conflict is refused.
func TestPoolSameNonceConflict(t *testing.T) {
	st := state.New()
	a, b := mkAddr(1), mkAddr(2)
	st.SetBalance(a, 1000)

	p := pool.New(st, 0)
	first := tx(a, b, 0, 1, 10)
	if err := p.Add(first); err != nil {
		t.Fatal(err)
	}
	// Exact same transaction -> pure duplicate.
	if err := p.Add(first); !errors.Is(err, pool.ErrAlreadyKnown) {
		t.Fatalf("exact duplicate: want ErrAlreadyKnown, got %v", err)
	}
	// Same (sender,nonce), different content (other fee) -> conflict, refused.
	conflict := tx(a, b, 0, 1, 99)
	if err := p.Add(conflict); !errors.Is(err, pool.ErrSameNonceConflict) {
		t.Fatalf("same-nonce conflict: want ErrSameNonceConflict, got %v", err)
	}
	if p.Has(conflict) {
		t.Fatal("conflicting replacement must not occupy the slot")
	}
	if !p.Has(first) {
		t.Fatal("the original transaction must remain in the slot")
	}
	// Conflict differs from bad signature / insufficient / nonce errors.
	bad := tx(a, b, 1, 1, 10)
	bad.Sig[0] ^= 1
	if err := p.Add(bad); !errors.Is(err, state.ErrBadSignature) {
		t.Fatalf("bad sig: %v", err)
	}
	t.Logf("decision: duplicate=ErrAlreadyKnown, replacement=ErrSameNonceConflict, bad sig=ErrBadSignature (distinct)")

	// The next correct nonce still admits normally and packs in sequence.
	next := tx(a, b, 1, 1, 10)
	if err := p.Add(next); err != nil {
		t.Fatalf("next nonce: %v", err)
	}
	out := p.Ordered(10)
	if len(out) != 2 || out[0] != first || out[1] != next {
		t.Fatalf("packed order wrong: %d txs", len(out))
	}
}

// TestPoolRebaseDeterministic: two independent pools that received the same
// transactions in DIFFERENT orders must, after rebasing onto an identical new
// head state, retain the same survivor set and pack blocks in the same order.
func TestPoolRebaseDeterministic(t *testing.T) {
	base := state.New()
	a, b := mkAddr(1), mkAddr(2)
	base.SetBalance(a, 1000)
	base.SetBalance(b, 1000)

	txs := []*types.Tx{
		tx(a, b, 0, 1, 5),
		tx(a, b, 1, 1, 50),
		tx(a, b, 2, 1, 9),
		tx(b, a, 0, 1, 7),
	}
	p1 := pool.New(base, 0)
	p2 := pool.New(base, 0)
	for i := range txs {
		if err := p1.Add(txs[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(txs) - 1; i >= 0; i-- {
		if err := p2.Add(txs[i]); err != nil {
			t.Fatal(err)
		}
	}

	// New head state: both senders' nonce 0 got committed (balances adjusted
	// plausibly). Both pools must independently converge to the same set.
	newState := base.Clone()
	if err := newState.ApplyTx(txs[0]); err != nil { // a0 fee5
		t.Fatal(err)
	}
	if err := newState.ApplyTx(txs[3]); err != nil { // b0 fee7
		t.Fatal(err)
	}
	p1.Rebase(newState)
	p2.Rebase(newState)

	survivors := func(p *pool.Pool) map[types.Hash]bool {
		m := map[types.Hash]bool{}
		for _, e := range p.Pending() {
			m[types.HashBytes(types.CanonicalTx(e.Tx))] = true
		}
		return m
	}
	s1, s2 := survivors(p1), survivors(p2)
	if len(s1) != len(s2) {
		t.Fatalf("survivor count differs: %d vs %d", len(s1), len(s2))
	}
	for h := range s1 {
		if !s2[h] {
			t.Fatalf("survivor sets differ: %x kept on one node only", h[:8])
		}
	}
	o1, o2 := p1.Ordered(100), p2.Ordered(100)
	if len(o1) != len(o2) {
		t.Fatalf("packed length differs: %d vs %d", len(o1), len(o2))
	}
	for i := range o1 {
		if types.HashBytes(types.CanonicalTx(o1[i])) != types.HashBytes(types.CanonicalTx(o2[i])) {
			t.Fatalf("packed order differs at position %d", i)
		}
	}
	var fees []uint64
	for _, x := range o1 {
		fees = append(fees, x.Fee)
	}
	t.Logf("decision: after rebase survivors identical on both nodes; packed fees=%v (a1 fee50 first)", fees)
}
