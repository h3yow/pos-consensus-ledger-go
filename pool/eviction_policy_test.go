package pool_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// pendingKey is a (sender,nonce) snapshot used to assert a rejected Add changes
// absolutely nothing in the pool.
type pendingKey struct {
	from  types.Address
	nonce uint64
}

func snapshot(p *pool.Pool) map[pendingKey]types.Hash {
	m := map[pendingKey]types.Hash{}
	for _, e := range p.Pending() {
		m[pendingKey{e.Tx.From, e.Tx.Nonce}] = types.HashBytes(types.CanonicalTx(e.Tx))
	}
	return m
}

func assertSnapshotUnchanged(t *testing.T, p *pool.Pool, before map[pendingKey]types.Hash, label string) {
	t.Helper()
	after := snapshot(p)
	if len(after) != len(before) {
		t.Fatalf("%s: rejected add changed pool size %d -> %d", label, len(before), len(after))
	}
	for k, h := range before {
		if after[k] != h {
			t.Fatalf("%s: rejected add changed slot %+v", label, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Fatalf("%s: rejected add added slot %+v", label, k)
		}
	}
}

// TestEqualFeeEarlierWins pins the tie-break under pressure: at equal fee the
// EARLIER arrival is retained and the later one rejected; rejection leaves the
// pool byte-for-byte unchanged.
func TestEqualFeeEarlierWins(t *testing.T) {
	st := state.New()
	a, b, c, d, e := mkAddr(1), mkAddr(2), mkAddr(3), mkAddr(4), mkAddr(5)
	for _, x := range []types.Address{a, b, c, d, e} {
		st.SetBalance(x, 1_000_000)
	}

	p := pool.New(st, 3)
	// Three distinct-sender tails, all fee 10, submitted in a known order.
	a0 := tx(a, c, 0, 1, 10)
	b0 := tx(b, c, 0, 1, 10)
	c0 := tx(c, a, 0, 1, 10)
	for _, x := range []*types.Tx{a0, b0, c0} {
		if err := p.Add(x); err != nil {
			t.Fatal(err)
		}
	}
	// A fourth sender, same fee, later arrival: it loses the tie to ALL three
	// eligible occupants and is refused with ErrPoolFull.
	d0 := tx(d, a, 0, 1, 10)
	before := snapshot(p)
	if err := p.Add(d0); !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("equal-fee latecomer: want ErrPoolFull, got %v", err)
	}
	assertSnapshotUnchanged(t, p, before, "equal-fee latecomer rejected")
	t.Logf("input: full equal-fee tails, later equal-fee newcomer -> REJECT ErrPoolFull (earlier arrivals protected)")

	// A strictly higher fee evicts the lowest-priority eligible tail; with
	// three equal-fee occupants the hash tie-break decides WHICH tail leaves,
	// but exactly one leaves and the survivor set stays gap-free.
	hi := tx(e, a, 0, 1, 11)
	if err := p.Add(hi); err != nil {
		t.Fatalf("higher-fee newcomer: %v", err)
	}
	if p.Size() != 3 || !p.Has(hi) {
		t.Fatalf("after higher-fee add size=%d hasHi=%v, want 3/true", p.Size(), p.Has(hi))
	}
	kept := 0
	for _, x := range []*types.Tx{a0, b0, c0} {
		if p.Has(x) {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("want exactly 2 of 3 equal-fee occupants retained, got %d", kept)
	}
	t.Logf("input: higher-fee newcomer fee11 -> ACCEPT, exactly one equal-fee tail evicted (hash tie-break)")
}

// TestMultiSenderMixedPressure mixes several senders, some with contiguous
// runs and some with single tails, and asserts the global priority rule only
// ever evicts occupants whose eviction opens no new gap.
func TestMultiSenderMixedPressure(t *testing.T) {
	st := state.New()
	senders := make([]types.Address, 5)
	for i := range senders {
		senders[i] = mkAddr(byte(10 + i))
		st.SetBalance(senders[i], 1_000_000)
	}
	dest := mkAddr(99)
	const cap = 6
	p := pool.New(st, cap)

	// Occupants (fill order matters for equal-fee ties):
	//  s0: nonces 0,1 fee10 (run, nonce1 is its tail)
	//  s1: nonce 0 fee10     (single tail)
	//  s2: nonces 0,1 fee10  (run)
	//  s3: nonce 0 fee1      (single tail, globally cheapest)
	type sub struct {
		sender int
		nonce  uint64
		fee    uint64
	}
	plan := []sub{
		{0, 0, 10}, {0, 1, 10},
		{1, 0, 10},
		{2, 0, 10}, {2, 1, 10},
		{3, 0, 1},
	}
	mk := func(s sub) *types.Tx { return tx(senders[s.sender], dest, s.nonce, 1, s.fee) }
	for _, s := range plan {
		if err := p.Add(mk(s)); err != nil {
			t.Fatalf("fill %+v: %v", s, err)
		}
	}

	// Newcomer s4n0 fee20: the lowest-priority eligible occupant is s3n0
	// (fee1), a single tail — it must be evicted, every run untouched.
	incoming := tx(senders[4], dest, 0, 1, 20)
	if err := p.Add(incoming); err != nil {
		t.Fatalf("s4 fee20: %v", err)
	}
	if p.Has(tx(senders[3], dest, 0, 1, 1)) {
		t.Fatal("cheapest eligible tail s3n0 must be evicted")
	}
	if !p.Has(tx(senders[0], dest, 0, 1, 10)) || !p.Has(tx(senders[0], dest, 1, 1, 10)) {
		t.Fatal("sender s0 run must survive intact")
	}
	if !p.Has(tx(senders[2], dest, 0, 1, 10)) || !p.Has(tx(senders[2], dest, 1, 1, 10)) {
		t.Fatal("sender s2 run must survive intact")
	}
	if p.Size() != cap {
		t.Fatalf("size %d, want %d", p.Size(), cap)
	}
	t.Logf("input: multi-sender full cap6, s4n0 fee20 -> ACCEPT, evicted cheapest eligible tail s3n0(fee1); no run gapped")

	// Now holds s0{0,1}, s1{0}, s2{0,1}, s4{0}. s0's next nonce 2 at fee1000
	// is allowed in: it cannot displace s0's own prefix, but another sender's
	// eligible tail can pay the room. The lowest-priority such tail is a fee10
	// one (s1n0 or s2n1, hash tie-break), never the fee20 s4n0.
	s0n2 := tx(senders[0], dest, 2, 1, 1000)
	if err := p.Add(s0n2); err != nil {
		t.Fatalf("s0n2 fee1000 should take room from another sender's tail: %v", err)
	}
	if !p.Has(s0n2) {
		t.Fatal("s0n2 must be admitted")
	}
	if !p.Has(tx(senders[0], dest, 0, 1, 10)) || !p.Has(tx(senders[0], dest, 1, 1, 10)) {
		t.Fatal("s0 prefix must survive: s0 run stays contiguous {0,1,2}")
	}
	others := []*types.Tx{
		tx(senders[1], dest, 0, 1, 10),
		tx(senders[2], dest, 1, 1, 10),
		tx(senders[4], dest, 0, 1, 20),
	}
	gone := 0
	for _, x := range others {
		if !p.Has(x) {
			gone++
		}
	}
	if gone != 1 {
		t.Fatalf("want exactly 1 other-sender tail evicted, got %d", gone)
	}
	if !p.Has(tx(senders[4], dest, 0, 1, 20)) {
		t.Fatal("fee20 tail s4n0 must outrank fee10 tails")
	}
	if p.Size() != cap {
		t.Fatalf("size %d, want %d", p.Size(), cap)
	}
	t.Logf("input: s0n2 fee1000 -> ACCEPT at one fee10 other-sender tail's expense; s0={0,1,2} contiguous, cap respected")
}

// TestRejectionSideEffects enumerates every rejection class on a full pool and
// verifies the pool is identical before and after (contents and capacity).
func TestRejectionSideEffects(t *testing.T) {
	st := state.New()
	a, b, poor, rich := mkAddr(1), mkAddr(2), mkAddr(6), mkAddr(7)
	st.SetBalance(a, 1_000_000)
	st.SetBalance(b, 1_000_000)
	st.SetBalance(poor, 100)
	st.SetBalance(rich, 1_000_000)
	p := pool.New(st, 2)
	a0 := tx(a, b, 0, 1, 10)
	a1 := tx(a, b, 1, 1, 10)
	if err := p.Add(a0); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(a1); err != nil {
		t.Fatal(err)
	}

	badSig := tx(rich, b, 0, 1, 5000)
	badSig.Sig[0] ^= 1

	cases := []struct {
		name string
		t    *types.Tx
		want error
	}{
		{"would-gap: same sender next nonce even at fee1000", tx(a, b, 2, 1, 1000), pool.ErrWouldGapNonce},
		{"outbid: valid funded newcomer fee1", tx(rich, b, 0, 1, 1), pool.ErrPoolFull},
		{"pure duplicate", a1, pool.ErrAlreadyKnown},
		{"same-nonce different-content conflict", types.SignTx(&types.Tx{From: a, To: b, Nonce: 0, Value: 1, Fee: 99}), pool.ErrSameNonceConflict},
		{"bad signature", badSig, state.ErrBadSignature},
		{"insufficient balance", tx(poor, a, 0, 1, 5000), state.ErrInsufficient},
	}
	for _, tc := range cases {
		before := snapshot(p)
		err := p.Add(tc.t)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.want, err)
		}
		assertSnapshotUnchanged(t, p, before, tc.name)
		t.Logf("input: %s -> REJECT (%v); pool unchanged size=%d", tc.name, err, p.Size())
	}

	// Genuine nonce-too-low against committed state: rebase onto a state where
	// a's nonce0 already executed, then replay it.
	st2 := state.New()
	st2.SetBalance(a, 1_000_000)
	if err := st2.ApplyTx(a0); err != nil { // commits nonce0
		t.Fatal(err)
	}
	p.Rebase(st2) // a1 (nonce1) remains pending
	before := snapshot(p)
	if err := p.Add(a0); !errors.Is(err, state.ErrNonceTooLow) {
		t.Fatalf("nonce too low: want ErrNonceTooLow, got %v", err)
	}
	assertSnapshotUnchanged(t, p, before, "nonce too low post-rebase")
	t.Logf("input: replayed a0 after rebase -> REJECT (ErrNonceTooLow); pool unchanged size=%d", p.Size())

	// All rejection reasons are mutually distinct sentinels.
	errs := []error{pool.ErrWouldGapNonce, pool.ErrPoolFull, pool.ErrAlreadyKnown,
		pool.ErrSameNonceConflict, state.ErrBadSignature, state.ErrInsufficient,
		state.ErrNonceTooLow, state.ErrNonceTooHigh, state.ErrOverflows}
	for i := range errs {
		for j := i + 1; j < len(errs); j++ {
			if errors.Is(errs[i], errs[j]) {
				t.Fatalf("rejection sentinels %v and %v must be distinct", errs[i], errs[j])
			}
		}
	}
	t.Logf("decision: all %d rejection reasons are distinct sentinels", len(errs))
}
