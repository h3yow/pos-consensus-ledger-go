package pool_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// pendingNonces returns the sorted pending nonces of one sender.
func pendingNonces(p *pool.Pool, from types.Address) []uint64 {
	var out []uint64
	for _, e := range p.Pending() {
		if e.Tx.From == from {
			out = append(out, e.Tx.Nonce)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// TestPoolNextNonceNeverGaps is the minimal regression for the reported bug:
// a full pool must never evict the very nonce a newcomer depends on. cap=1,
// hold nonce 0, submit nonce 1: nonce 1 must be REFUSED (ErrPoolFull) and
// nonce 0 must stay — admitting 1 while dropping 0 would strand nonce 1
// forever behind a gap.
func TestPoolNextNonceNeverGaps(t *testing.T) {
	st := state.New()
	a, b := mkAddr(1), mkAddr(2)
	st.SetBalance(a, 100000)
	st.SetBalance(b, 100000)

	// --- minimal case: capacity 1 ---
	p := pool.New(st, 1)
	a0 := tx(a, b, 0, 1, 10)
	a1 := tx(a, b, 1, 1, 20) // higher fee, but depends on a0
	if err := p.Add(a0); err != nil {
		t.Fatal(err)
	}
	t.Logf("input: cap=1, hold a0(fee10); submit a1(fee20)")
	err := p.Add(a1)
	if !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("a1: want ErrPoolFull, got %v", err)
	}
	if !p.Has(a0) || p.Has(a1) || p.Size() != 1 {
		t.Fatalf("pool changed on refusal: has(a0)=%v has(a1)=%v size=%d",
			p.Has(a0), p.Has(a1), p.Size())
	}
	t.Logf("decision: a1 refused with ErrPoolFull (evicting a0 would gap the sender); a0 kept, size=1")

	// --- reported case: full pool {a0,a1,b0}, newcomer a2 ---
	// a2 must evict the OTHER sender's tail (b0), never a1.
	p2 := pool.New(st, 3)
	b0 := tx(b, a, 0, 1, 15)
	for _, x := range []*types.Tx{tx(a, b, 0, 1, 10), tx(a, b, 1, 1, 10), b0} {
		if err := p2.Add(x); err != nil {
			t.Fatal(err)
		}
	}
	a2 := tx(a, b, 2, 1, 20)
	t.Logf("input: cap=3, hold a0(fee10) a1(fee10) b0(fee15); submit a2(fee20)")
	if err := p2.Add(a2); err != nil {
		t.Fatalf("a2 should displace b0 (other sender's tail): %v", err)
	}
	if p2.Has(b0) {
		t.Fatal("b0 (lowest-priority eligible tail) should have been evicted")
	}
	got := pendingNonces(p2, a)
	if fmt.Sprint(got) != "[0 1 2]" {
		t.Fatalf("sender a nonces = %v, want [0 1 2] (no gap)", got)
	}
	t.Logf("decision: a2 admitted, b0 evicted; sender a pending nonces=%v (contiguous)", got)

	// --- same situation but the other tail is too rich: refuse whole ---
	p3 := pool.New(st, 3)
	rich := tx(b, a, 0, 1, 100)
	before := []*types.Tx{tx(a, b, 0, 1, 10), tx(a, b, 1, 1, 10), rich}
	for _, x := range before {
		if err := p3.Add(x); err != nil {
			t.Fatal(err)
		}
	}
	a2cheap := tx(a, b, 2, 1, 20) // outbids a0/a1 but they are protected
	t.Logf("input: cap=3, hold a0(fee10) a1(fee10) b0(fee100); submit a2(fee20)")
	if err := p3.Add(a2cheap); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("a2: want ErrPoolFull, got %v", err)
	}
	for _, x := range before {
		if !p3.Has(x) {
			t.Fatalf("refused add must not evict anything: lost nonce=%d fee=%d", x.Nonce, x.Fee)
		}
	}
	if p3.Size() != 3 {
		t.Fatalf("size = %d, want 3 (capacity not exceeded, nothing lost)", p3.Size())
	}
	t.Logf("decision: a2 refused (only b0 eligible, fee100 > 20); all 3 originals kept, size=3")
}

// TestPoolEqualFeeFirstComeFirstKept pins the tie-break: at equal fee the
// EARLIER arrival is protected and the newest one loses, deterministically.
func TestPoolEqualFeeFirstComeFirstKept(t *testing.T) {
	st := state.New()
	a, b, c := mkAddr(1), mkAddr(2), mkAddr(3)
	for _, x := range []types.Address{a, b, c} {
		st.SetBalance(x, 100000)
	}
	p := pool.New(st, 2)
	a0 := tx(a, b, 0, 1, 10) // arrives first
	b0 := tx(b, a, 0, 1, 10) // arrives second, same fee
	if err := p.Add(a0); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(b0); err != nil {
		t.Fatal(err)
	}

	// Same-fee newcomer loses the tie (it is the newest) and is refused.
	c0 := tx(c, a, 0, 1, 10)
	t.Logf("input: cap=2 full of fee10 (a0 then b0); submit c0(fee10)")
	if err := p.Add(c0); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("equal-fee newcomer: want ErrPoolFull, got %v", err)
	}
	if !p.Has(a0) || !p.Has(b0) || p.Size() != 2 {
		t.Fatal("equal-fee refusal must leave the pool untouched")
	}
	t.Logf("decision: c0 refused (newest at equal fee); a0,b0 kept")

	// Higher-fee newcomer evicts the LATER of the equal-fee occupants (b0).
	c1 := tx(c, a, 0, 1, 11)
	t.Logf("input: submit c0'(fee11)")
	if err := p.Add(c1); err != nil {
		t.Fatal(err)
	}
	if !p.Has(a0) || p.Has(b0) || !p.Has(c1) {
		t.Fatalf("want {a0, c1}: a0=%v b0=%v c1=%v", p.Has(a0), p.Has(b0), p.Has(c1))
	}
	t.Logf("decision: fee11 evicted b0 (later arrival at equal fee); a0 (earlier) protected")
}

// TestPoolCappedRebaseConvergence: two pools with the SAME capacity receive
// the SAME mixed-sender stream (including next-nonce submissions that trigger
// the gap protection), so they take identical eviction decisions; after a
// block commits the packed prefix, both rebase to identical survivor sets and
// identical next-block order.
func TestPoolCappedRebaseConvergence(t *testing.T) {
	base := state.New()
	a, b := mkAddr(1), mkAddr(2)
	base.SetBalance(a, 100000)
	base.SetBalance(b, 100000)

	stream := []*types.Tx{
		tx(a, b, 0, 1, 10),
		tx(a, b, 1, 1, 10),
		tx(b, a, 0, 1, 15),
		tx(a, b, 2, 1, 20), // next nonce of a under pressure: must not gap a
		tx(b, a, 1, 1, 12),
		tx(a, b, 3, 1, 1), // too cheap: refused on both nodes alike
	}
	const cap = 4
	pA := pool.New(base, cap)
	pB := pool.New(base.Clone(), cap)
	for i, x := range stream {
		errA, errB := pA.Add(x), pB.Add(x)
		if (errA == nil) != (errB == nil) {
			t.Fatalf("tx %d: nodes disagree on admission: %v vs %v", i, errA, errB)
		}
		t.Logf("input: tx %d (from=%x nonce=%d fee=%d) -> nodeA=%v nodeB=%v",
			i, x.From[:2], x.Nonce, x.Fee, errA, errB)
	}

	// Both nodes pack the same block and commit it to their head states.
	blk := pA.Ordered(100)
	stA, stB := base.Clone(), base.Clone()
	for _, x := range blk {
		if err := stA.ApplyTx(x); err != nil {
			t.Fatal(err)
		}
		if err := stB.ApplyTx(x); err != nil {
			t.Fatal(err)
		}
	}
	pA.Rebase(stA)
	pB.Rebase(stB)

	survA, survB := pA.Pending(), pB.Pending()
	if len(survA) != len(survB) {
		t.Fatalf("survivor counts differ: %d vs %d", len(survA), len(survB))
	}
	for i := range survA {
		ha := types.HashBytes(types.CanonicalTx(survA[i].Tx))
		hb := types.HashBytes(types.CanonicalTx(survB[i].Tx))
		if ha != hb {
			t.Fatalf("survivor %d differs between nodes", i)
		}
	}
	oA, oB := pA.Ordered(100), pB.Ordered(100)
	if len(oA) != len(oB) {
		t.Fatalf("next-block length differs: %d vs %d", len(oA), len(oB))
	}
	for i := range oA {
		if types.HashBytes(types.CanonicalTx(oA[i])) != types.HashBytes(types.CanonicalTx(oB[i])) {
			t.Fatalf("next-block order differs at %d", i)
		}
	}
	// And the survivors must still be gap-free per sender.
	for _, from := range []types.Address{a, b} {
		got := pendingNonces(pA, from)
		for i, n := range got {
			want := stA.GetNonce(from) + uint64(i)
			if n != want {
				t.Fatalf("sender %x pending nonces %v have a gap (want prefix from %d)",
					from[:2], got, stA.GetNonce(from))
			}
		}
	}
	t.Logf("decision: after block+rebase both nodes keep %d survivors, identical next-block order, no gaps",
		len(survA))
}
