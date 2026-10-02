package pool_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func pAddr(i byte) types.Address {
	var a types.Address
	a[19] = i
	return a
}

// pTx builds a well-formed signed tx from->addr99.
func pTx(from types.Address, nonce, fee uint64) *types.Tx {
	var to types.Address
	to[19] = 99
	return types.SignTx(&types.Tx{From: from, To: to, Nonce: nonce, Value: 1, Fee: fee})
}

// TestPoolCapacityEviction fills a tiny pool and checks the predictable
// eviction rule: higher fee is always retained; equal fee keeps the earlier
// arrival; and eviction never leaves a sender's queued sequence with a hole.
func TestPoolCapacityEviction(t *testing.T) {
	st := state.New()
	a := pAddr(1)
	b := pAddr(2)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)
	p := pool.NewWithCap(st, 3)

	// Fill to capacity: A nonces 0,1,2 all fee 10, in arrival order.
	t0 := pTx(a, 0, 10)
	t1 := pTx(a, 1, 10)
	t2 := pTx(a, 2, 10)
	for _, tx := range []*types.Tx{t0, t1, t2} {
		if err := p.Add(tx); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	if p.Size() != 3 {
		t.Fatalf("size = %d, want 3", p.Size())
	}

	// A lower-fee newcomer (fee 1) cannot displace anything -> rejected,
	// pool byte-for-byte unchanged.
	if err := p.Add(pTx(b, 0, 1)); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("low-fee newcomer: want ErrPoolFull, got %v", err)
	}
	if p.Size() != 3 {
		t.Fatalf("size after rejected add = %d, want 3", p.Size())
	}

	// A higher-fee newcomer evicts the weakest incumbent. With equal fee10
	// the newest arrival (A nonce 2) loses; it is the suffix tail, so A's
	// surviving nonces 0,1 stay contiguous.
	if err := p.Add(pTx(b, 0, 50)); err != nil {
		t.Fatalf("high-fee newcomer: %v", err)
	}
	if p.Size() != 3 {
		t.Fatalf("size = %d, want 3", p.Size())
	}
	got := p.Ordered(10)
	if len(got) != 3 || got[0].Fee != 50 || got[1].Nonce != 0 || got[2].Nonce != 1 {
		t.Fatalf("ordered = %+v, want [b0 fee50, a0 fee10, a1 fee10]", got)
	}
	t.Logf("decision: fee50 admitted by evicting newest fee10 (A nonce2); A keeps contiguous nonces 0,1; size=3")
}

// TestPoolEvictionCascadeRemovesSuffix asserts the suffix cascade: when the
// lowest-priority victim has later queued nonces from the same sender, the
// whole suffix is removed so surviving queued nonces never have a gap.
func TestPoolEvictionCascadeRemovesSuffix(t *testing.T) {
	st := state.New()
	a := pAddr(1)
	b := pAddr(2)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)
	p := pool.NewWithCap(st, 3)

	// A holds the contiguous run nonces 0,1,2 (fee 10): pool full.
	for _, n := range []uint64{0, 1, 2} {
		if err := p.Add(pTx(a, n, 10)); err != nil {
			t.Fatal(err)
		}
	}
	// Force the victim to be the MIDDLE nonce by giving a later-arriving
	// same-fee situation: submit A nonce 1 replacement is impossible (slot
	// occupied), so instead evict from the middle via a high-fee newcomer
	// when the tail carries the globally-weakest priority. The weakest
	// fee10 entry is the newest arrival overall: that is nonce 2 (tail), so
	// first verify the plain-tail case keeps 0,1.
	if err := p.Add(pTx(b, 0, 100)); err != nil {
		t.Fatal(err)
	}
	// Pool: b0(100), a0(10), a1(10). Rebuild to hit a true middle victim:
	p = pool.NewWithCap(st, 4)
	// Arrival order matters: make A nonce 1 arrive LATEST among A's fee10
	// run by inserting a higher-fee anchor between, so when a fee50 tx later
	// forces eviction the weakest fee10 is nonce1, not the tail.
	if err := p.Add(pTx(a, 0, 10)); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(pTx(a, 2, 10)); err != nil { // gap 1 queued (allowed pre-block)
		t.Fatal(err)
	}
	if err := p.Add(pTx(b, 0, 100)); err != nil { // anchor survives
		t.Fatal(err)
	}
	if err := p.Add(pTx(a, 1, 10)); err != nil { // newest fee10 = nonce1
		t.Fatal(err)
	}
	if p.Size() != 4 {
		t.Fatalf("setup size = %d want 4", p.Size())
	}
	// High-fee newcomer -> weakest fee10 is A nonce1 (latest arrival). Its
	// suffix {nonce 1, nonce 2} must both be removed to avoid a hole, leaving
	// only A nonce 0.
	if err := p.Add(pTx(b, 1, 50)); err != nil {
		t.Fatalf("cascade newcomer: %v", err)
	}
	survivors := map[uint64]bool{}
	for _, tx := range p.Ordered(10) {
		if tx.From == a {
			survivors[tx.Nonce] = true
		}
	}
	if !survivors[0] || survivors[1] || survivors[2] {
		t.Fatalf("after cascade expected only A nonce0 to survive, got %v", survivors)
	}
	t.Logf("decision: victim A nonce1 cascaded to suffix {1,2}; surviving A nonces {0} (no gap); size=%d", p.Size())
}

// TestPoolSameNonceConflict ensures one (sender,nonce) slot holds exactly one
// transaction: an identical tx is ErrAlreadyKnown, a DIFFERENT tx for the
// same slot is ErrSameNonceConflict, both distinct from signature/balance/
// nonce rejection classes.
func TestPoolSameNonceConflict(t *testing.T) {
	st := state.New()
	a := pAddr(1)
	st.SetBalance(a, 1_000_000)
	p := pool.NewWithCap(st, 10)

	first := pTx(a, 0, 10)
	if err := p.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(first); !errors.Is(err, pool.ErrAlreadyKnown) {
		t.Fatalf("identical: want ErrAlreadyKnown, got %v", err)
	}
	// Same sender+nonce, different content (different fee => different hash).
	conflict := types.SignTx(&types.Tx{From: a, To: pAddr(50), Nonce: 0, Value: 1, Fee: 99})
	if err := p.Add(conflict); !errors.Is(err, pool.ErrSameNonceConflict) {
		t.Fatalf("conflict: want ErrSameNonceConflict, got %v", err)
	}
	if p.Size() != 1 {
		t.Fatalf("size = %d want 1 (incumbent untouched)", p.Size())
	}
	// Bad signature stays a distinct class even at an occupied-looking nonce.
	bad := pTx(a, 4, 10)
	bad.Sig[0] ^= 1
	if err := p.Add(bad); !errors.Is(err, state.ErrBadSignature) {
		t.Fatalf("bad sig: want ErrBadSignature, got %v", err)
	}
	// Insufficient balance distinct from conflict.
	poor := types.SignTx(&types.Tx{From: a, To: pAddr(50), Nonce: 2, Value: 1_000_000, Fee: 1_000_000})
	if err := p.Add(poor); !errors.Is(err, state.ErrInsufficient) {
		t.Fatalf("insufficient: want ErrInsufficient, got %v", err)
	}
	// Commit nonce 0 against the state, then submit a different tx with the
	// now-replayed nonce: distinct from duplicate and conflict classes.
	if err := st.ApplyTx(first); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(types.SignTx(&types.Tx{From: a, To: pAddr(51), Nonce: 0, Value: 2, Fee: 10})); !errors.Is(err, state.ErrNonceTooLow) {
		t.Fatalf("replay: want ErrNonceTooLow, got %v", err)
	}
	t.Logf("decision: duplicate=ErrAlreadyKnown, replacement=ErrSameNonceConflict, distinct from sig/balance/nonce errors")
}
