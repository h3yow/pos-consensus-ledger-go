package pool_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestEvictionNeverGapsSameSender is the regression for the reported bug:
// with a full pool, submitting the SAME sender's very next nonce must never be
// admitted by dropping the nonce it directly continues. The cap=1 case
// (accept nonce0, then nonce1) is the smallest possible instance.
func TestEvictionNeverGapsSameSender(t *testing.T) {
	st := state.New()
	a, b := mkAddr(1), mkAddr(2)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)

	// --- cap=1 minimal case ---
	p := pool.New(st, 1)
	a0 := tx(a, b, 0, 1, 10)
	if err := p.Add(a0); err != nil {
		t.Fatalf("a0: %v", err)
	}
	// Nonce 1, even with a much higher fee, cannot be kept without nonce 0.
	a1 := tx(a, b, 1, 1, 1000)
	err := p.Add(a1)
	if !errors.Is(err, pool.ErrWouldGapNonce) {
		t.Fatalf("cap1 next-nonce: want ErrWouldGapNonce, got %v", err)
	}
	if p.Size() != 1 {
		t.Fatalf("cap1 size = %d, want 1 (rejected add changes nothing)", p.Size())
	}
	if !p.Has(a0) {
		t.Fatal("cap1: nonce0 must remain; it must never be dropped for nonce1")
	}
	if p.Has(a1) {
		t.Fatal("cap1: nonce1 must not be admitted without nonce0")
	}
	t.Logf("input: cap=1 full(a0 fee10), submit a1 fee1000 -> REJECT ErrWouldGapNonce; pool unchanged")

	// A lower-fee unrelated tx also cannot enter: it cannot outbid a0, so it
	// loses the priority comparison and is rejected; a0 stays put either way.
	b0lo := tx(b, a, 0, 1, 1)
	if err := p.Add(b0lo); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("cap1 b0 low: want ErrPoolFull, got %v", err)
	}
	if !p.Has(a0) || p.Size() != 1 {
		t.Fatal("a0 must still be present, size still 1")
	}
	t.Logf("input: cap=1 full(a0 fee10), submit b0 fee1 -> REJECT ErrPoolFull (outbid); pool unchanged")

	// A higher-fee unrelated tx CAN take the single slot: a keeps no higher
	// nonce, so dropping a0 does not create a gap for a.
	b0hi := tx(b, a, 0, 1, 100)
	if err := p.Add(b0hi); err != nil {
		t.Fatalf("cap1 b0 high: %v", err)
	}
	if p.Has(a0) {
		t.Fatal("a0 should be evicted by higher-fee b0 (a keeps no later nonce: no gap)")
	}
	if !p.Has(b0hi) {
		t.Fatal("b0 high should be admitted")
	}
	t.Logf("input: cap=1 submit b0 fee100 -> ACCEPT, evicted a0 (no higher nonce of a kept, no gap)")

	// --- cap=3 contiguous run, next nonce from same sender ---
	p3 := pool.New(st, 3)
	c := mkAddr(3)
	st.SetBalance(c, 1_000_000)
	c0 := tx(c, a, 0, 1, 10)
	c1 := tx(c, a, 1, 1, 10)
	c2 := tx(c, a, 2, 1, 10)
	for _, x := range []*types.Tx{c0, c1, c2} {
		if err := p3.Add(x); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	// c3 with a huge fee: old code evicted c2 (sender c's tail) and kept c3,
	// leaving {c0,c1,c3} — a permanent gap, c3 never packs and holds capacity.
	c3 := tx(c, a, 3, 1, 500)
	if err := p3.Add(c3); !errors.Is(err, pool.ErrWouldGapNonce) {
		t.Fatalf("c3: want ErrWouldGapNonce, got %v", err)
	}
	for _, x := range []*types.Tx{c0, c1, c2} {
		if !p3.Has(x) {
			t.Fatalf("rejected c3 must not evict anything; missing nonce %d", x.Nonce)
		}
	}
	if p3.Has(c3) {
		t.Fatal("c3 must not be admitted")
	}
	if p3.Size() != 3 {
		t.Fatalf("size = %d, want 3", p3.Size())
	}
	t.Logf("input: cap=3 full(c0,c1,c2 fee10), submit c3 fee500 -> REJECT ErrWouldGapNonce; {0,1,2} intact")
}

// TestGapHealsByFillingSmallerNonce shows the interaction between an
// already-gapped future-nonce run and a smaller nonce arriving under pressure:
// eviction may only remove the tail of the set INCLUDING the newcomer.
func TestGapHealsByFillingSmallerNonce(t *testing.T) {
	st := state.New()
	a, b, d := mkAddr(1), mkAddr(2), mkAddr(4)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)
	st.SetBalance(d, 1_000_000)

	p := pool.New(st, 2)
	// Fill with an unrelated tail d0 (fee100) and a's nonce1 (fee100): a1
	// waits for a0 (a pre-existing gap, which admission is allowed to keep).
	d0 := tx(d, b, 0, 1, 100)
	a1 := tx(a, b, 1, 1, 100)
	if err := p.Add(d0); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(a1); err != nil {
		t.Fatal(err)
	}
	// a2 outbids d0; evicting d0 opens NO new gap (d keeps no higher nonce).
	a2 := tx(a, b, 2, 1, 200)
	if err := p.Add(a2); err != nil {
		t.Fatalf("a2 should evict d0 (d keeps no higher nonce): %v", err)
	}
	if p.Has(d0) {
		t.Fatal("d0 should be evicted by higher-fee a2")
	}
	if !p.Has(a1) || !p.Has(a2) {
		t.Fatal("a1,a2 should be pending")
	}
	t.Logf("input: full{d0 fee100,a1 fee100}, submit a2 fee200 -> ACCEPT evict d0 (no NEW gap)")

	// Now a0 arrives at cap2 with {a1,a2} present. Keeping all three is
	// impossible; only a2 (the tail of the set that WOULD include a0) is
	// evictable. a0 outbids a2 (300 > 200), so it is admitted, a2 leaves and
	// the remaining run {a0,a1} is contiguous and packs.
	a0 := tx(a, b, 0, 1, 300)
	if err := p.Add(a0); err != nil {
		t.Fatalf("a0: %v", err)
	}
	if !p.Has(a0) || !p.Has(a1) {
		t.Fatal("after a0 admission the run must be {a0,a1}")
	}
	if p.Has(a2) {
		t.Fatal("a2 (tail) should be evicted so no gap remains")
	}
	out := p.Ordered(10)
	if len(out) != 2 || out[0].Nonce != 0 || out[1].Nonce != 1 {
		var ns []uint64
		for _, t := range out {
			ns = append(ns, t.Nonce)
		}
		t.Fatalf("packed nonces = %v, want [0 1]", ns)
	}
	t.Logf("input: submit a0 fee300 -> ACCEPT evict tail a2; pending {a0,a1} packs as [0,1]")
}
