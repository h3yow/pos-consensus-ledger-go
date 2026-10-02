package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// pendingID identifies a pending tx independent of local arrival order.
type pendingID struct {
	from  types.Address
	nonce uint64
}

func poolSet(t *testing.T, p *pool.Pool) map[pendingID]types.Hash {
	t.Helper()
	m := map[pendingID]types.Hash{}
	for _, e := range p.Pending() {
		m[pendingID{e.Tx.From, e.Tx.Nonce}] = types.HashBytes(types.CanonicalTx(e.Tx))
	}
	return m
}

func assertPoolsConverged(t *testing.T, pools []*pool.Pool, label string) {
	t.Helper()
	ref := poolSet(t, pools[0])
	for i := 1; i < len(pools); i++ {
		got := poolSet(t, pools[i])
		if len(got) != len(ref) {
			t.Fatalf("%s: node %d pending count %d != node0 %d", label, i, len(got), len(ref))
		}
		for k, h := range ref {
			if got[k] != h {
				t.Fatalf("%s: node %d differs on slot %+v", label, i, k)
			}
		}
	}
	var refOrder []types.Hash
	for _, tx := range pools[0].Ordered(100) {
		refOrder = append(refOrder, types.HashBytes(types.CanonicalTx(tx)))
	}
	for i := 1; i < len(pools); i++ {
		got := pools[i].Ordered(100)
		if len(got) != len(refOrder) {
			t.Fatalf("%s: node %d packed count %d != %d", label, i, len(got), len(refOrder))
		}
		for j, tx := range got {
			if types.HashBytes(types.CanonicalTx(tx)) != refOrder[j] {
				t.Fatalf("%s: node %d pack order differs at %d", label, i, j)
			}
		}
	}
	t.Logf("decision: %s — %d nodes hold identical pending set (%d txs) and identical pack order",
		label, len(pools), len(ref))
}

// assertNoNewGap verifies an operation that removed slots kept no higher nonce
// of the same sender (the removal did not CREATE a gap).
func assertNoNewGap(t *testing.T, before, after map[pendingID]types.Hash, label string) {
	t.Helper()
	for k := range before {
		if _, still := after[k]; still {
			continue
		}
		for k2 := range after {
			if k2.from == k.from && k2.nonce > k.nonce {
				t.Fatalf("%s: nonce %d removed while higher nonce %d kept (NEW gap for %x)",
					label, k.nonce, k2.nonce, k.from[:6])
			}
		}
	}
}

// TestCappedPoolCrossNodeConvergence runs a tiny-capacity pool on three real
// nodes. DISTINCT single-tail inputs are submitted concurrently (goroutines) at
// a single ingress; the mutex linearizes them and, because every sender
// contributes one tx, the global fee rule makes the accepted set independent of
// goroutine scheduling. That set is gossiped in a fixed order through the
// deterministic simulated network. Verified: capacity never breached, eviction
// never gaps a sender, the two capacity rejects are DISTINCT sentinels with
// zero side effects, nodes converge on pending set and next-block order, and
// after a real block/rebase they still converge (a future-nonce survivor is
// retained), followed by the same-sender continuation refusal on every node.
// No real network or wall-clock timing is used; -v logs every input/decision.
func TestCappedPoolCrossNodeConvergence(t *testing.T) {
	const cap = 4
	c := newClusterWithUsers(t, 3, cap, map[types.Address]uint64{
		userAddr(91): 100000,
		userAddr(92): 100000,
		userAddr(93): 100000,
	})
	pools := []*pool.Pool{c.nodes[0].Pool(), c.nodes[1].Pool(), c.nodes[2].Pool()}

	u := userAddr(99)  // drives the same-sender continuation refusal (phase 5)
	w1 := userAddr(91) // extra funded single-tail senders (phase 1 pressure)
	w2 := userAddr(92)
	w3 := userAddr(93) // its nonce1 is the post-rebase survivor (phase 3)
	s1 := c.nodes[0].Addr
	s2 := c.nodes[1].Addr
	dest := c.nodes[2].Addr

	type in struct {
		from  types.Address
		nonce uint64
		fee   uint64
	}
	mkTx := func(x in) *types.Tx {
		return types.SignTx(&types.Tx{From: x.from, To: dest, Nonce: x.nonce, Value: 1, Fee: x.fee})
	}
	ingress := c.nodes[0]

	// admit submits one input at the ingress concurrently. The mutex linearizes
	// the adds; the accepted survivor set is read from the ingress pool after
	// the barrier, so the test never depends on which goroutine ran first.
	var wg sync.WaitGroup
	var accMu sync.Mutex
	nRej := 0
	admit := func(x in) {
		defer wg.Done()
		if err := ingress.Pool().Add(mkTx(x)); err != nil {
			accMu.Lock()
			nRej++
			accMu.Unlock()
			if !errors.Is(err, pool.ErrPoolFull) && !errors.Is(err, pool.ErrWouldGapNonce) {
				t.Errorf("input nonce=%d fee=%d unexpected rejection: %v", x.nonce, x.fee, err)
			}
		}
	}

	// Phase 0: commit w3's nonce0 in a preliminary block so that a later w3
	// nonce1 is a genuine FUTURE-nonce survivor after the phase-3 rebase.
	setup0 := types.SignTx(&types.Tx{From: w3, To: dest, Nonce: 0, Value: 1, Fee: 1})
	if err := ingress.Pool().Add(setup0); err != nil {
		t.Fatalf("setup w3n0: %v", err)
	}
	c.net.DeliverAll()
	pre := c.step()
	if got := c.nodes[0].StateAt().GetNonce(w3); got != 1 {
		t.Fatalf("w3 nonce after setup block = %d, want 1", got)
	}
	t.Logf("decision: setup block h=%d committed w3n0 (w3 on-chain nonce now 1)", pre.Height)

	// Phase 1: six DISTINCT single-tail senders at cap=4 with distinct fees.
	// Every tx is always an eligible tail, so the result is the pure global fee
	// rule — the four highest fees — independent of goroutine admission order:
	//   w2n0(13), w1n0(12), s2n0(11), s1n0(10) survive;
	//   u0 fee2 and w3n1 fee3 do NOT survive the pressure.
	phase1 := []in{
		{s1, 0, 10},
		{s2, 0, 11},
		{w1, 0, 12},
		{w2, 0, 13},
		{u, 0, 2},
		{w3, 1, 3}, // future nonce (w3 committed nonce0); cheap, loses pressure
	}
	for _, x := range phase1 {
		x := x
		wg.Add(1)
		go admit(x)
	}
	wg.Wait()

	if ingress.Pool().Size() != cap {
		t.Fatalf("ingress size=%d, want %d", ingress.Pool().Size(), cap)
	}
	survivors := []*types.Tx{}
	for _, e := range ingress.Pool().Pending() {
		survivors = append(survivors, e.Tx)
	}
	if len(survivors) != 4 {
		t.Fatalf("phase1 survivors=%d, want 4", len(survivors))
	}
	wantFees := map[uint64]bool{10: false, 11: false, 12: false, 13: false}
	for _, tx := range survivors {
		if _, ok := wantFees[tx.Fee]; ok {
			wantFees[tx.Fee] = true
		}
	}
	for fee, found := range wantFees {
		if !found {
			t.Fatalf("phase1 survivor fee=%d missing (cheap tails must not survive)", fee)
		}
	}
	sort.Slice(survivors, func(i, j int) bool {
		if survivors[i].From != survivors[j].From {
			return bytes.Compare(survivors[i].From[:], survivors[j].From[:]) < 0
		}
		return survivors[i].Nonce < survivors[j].Nonce
	})
	for _, tx := range survivors {
		for _, nd := range c.nodes[1:] {
			c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgTx, From: ingress.Name, Tx: tx})
		}
	}
	c.net.DeliverAll()
	for i, p := range pools {
		if p.Size() > cap {
			t.Fatalf("node %d exceeded cap: %d > %d", i, p.Size(), cap)
		}
	}
	assertPoolsConverged(t, pools, "after concurrent submissions + deterministic gossip")
	t.Logf("input: 6 distinct single-tail slots concurrently at cap=4 -> top-4 fees survive regardless of order; nodes converged")

	// Phase 2: seed w3's CONTIGUOUS waiting pair {w3n1,w3n2} (w3's committed
	// nonce is 1, so n1 is ready and n2 is one ahead). They must buy room from
	// the two lowest eligible tails (s1n0 fee10, s2n0 fee11) — never gapping
	// any sender. After this the pool is {w1n0,w2n0,w3n1,w3n2}.
	seed := []in{{w3, 1, 50}, {w3, 2, 60}}
	for _, x := range seed {
		if err := ingress.Pool().Add(mkTx(x)); err != nil {
			t.Fatalf("seed w3 nonce=%d: %v", x.nonce, err)
		}
	}
	for _, x := range seed {
		tx := mkTx(x)
		for _, nd := range c.nodes[1:] {
			c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgTx, From: ingress.Name, Tx: tx})
		}
	}
	c.net.DeliverAll()
	assertPoolsConverged(t, pools, "after seeding w3 pair {n1,n2} (each buys room from a cheap tail)")
	if ingress.Pool().Has(mkTx(in{s1, 0, 10})) || ingress.Pool().Has(mkTx(in{s2, 0, 11})) {
		t.Fatal("s1n0/s2n0 should have been displaced by the higher-fee w3 pair")
	}
	if !ingress.Pool().Has(mkTx(in{w3, 1, 50})) || !ingress.Pool().Has(mkTx(in{w3, 2, 60})) {
		t.Fatal("w3 pair {n1,n2} must both be pending and contiguous")
	}
	t.Logf("input: w3n1 fee50,w3n2 fee60 -> ACCEPT, displaced two cheap tails; pool {w1n0,w2n0,w3n1,w3n2}, w3 contiguous")

	// The cheap distinct-tail rejection class, repeated on every node: u0 fee2
	// is a fresh valid tx (u nonce0 never present); it loses to every eligible
	// tail -> ErrPoolFull, nothing changes.
	low := in{u, 0, 2}
	for i, p := range pools {
		before := poolSet(t, p)
		err := p.Add(mkTx(low))
		if !errors.Is(err, pool.ErrPoolFull) {
			t.Fatalf("node %d low-fee add: want ErrPoolFull, got %v", i, err)
		}
		after := poolSet(t, p)
		assertNoNewGap(t, before, after, "low-fee rejected")
		if len(after) != len(before) {
			t.Fatalf("node %d rejected add changed pool size", i)
		}
		t.Logf("input: node %d cheap tail u0 fee2 -> REJECT ErrPoolFull; pool unchanged size=%d", i, p.Size())
	}

	// Phase 3: real block + rebase. Executable: w1n0,w2n0 (nonce0) and w3n1
	// (w3 committed nonce1) all pack; w3n2 is one ahead and SURVIVES as the
	// waiting future nonce. Every node rebases identically.
	b := c.step()
	c.assertSameHead()
	for i := range c.nodes {
		if c.nodes[i].Head().StateRoot != b.StateRoot {
			t.Fatalf("node %d state root mismatch", i)
		}
	}
	assertPoolsConverged(t, pools, fmt.Sprintf("after block h=%d + rebase (w3n2 survives)", b.Height))
	var packed []string
	for _, tx := range b.Txs {
		h := types.HashBytes(types.CanonicalTx(tx))
		packed = append(packed, fmt.Sprintf("%x:n%d", h[:6], tx.Nonce))
	}
	t.Logf("decision: block h=%d packed %v; w3n2 survives as the future nonce; identical head/root", b.Height, packed)
	if !ingress.Pool().Has(mkTx(in{w3, 2, 60})) {
		t.Fatal("w3n2 must survive rebase as the waiting future nonce")
	}

	// Phase 4: post-rebase, fill the pool with u's contiguous run
	// {u0,u1,u2,u3}. w3n2 currently occupies one slot; u3 (the run's highest
	// fee) displaces it (w3 then keeps no higher pending nonce — no gap), and
	// u0..u2 take the remaining room, ending with u's contiguous {0,1,2,3}.
	run := []in{{u, 0, 1000}, {u, 1, 1000}, {u, 2, 1000}, {u, 3, 2000}}
	for _, x := range run {
		if err := ingress.Pool().Add(mkTx(x)); err != nil {
			t.Fatalf("post-rebase run nonce=%d: %v", x.nonce, err)
		}
	}
	for _, x := range run {
		tx := mkTx(x)
		for _, nd := range c.nodes[1:] {
			c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgTx, From: ingress.Name, Tx: tx})
		}
	}
	c.net.DeliverAll()
	assertPoolsConverged(t, pools, "after post-rebase single-sender run fills the pool")
	for i, p := range pools {
		if p.Size() != cap {
			t.Fatalf("node %d size=%d, want full cap %d", i, p.Size(), cap)
		}
	}

	// Phase 5: u's next nonce (u4) at a high fee must be refused with
	// ErrWouldGapNonce on EVERY node: accepting could only be paid for by
	// dropping u's own required prefix. Nothing changes anywhere.
	next := in{u, 4, 900}
	for i, p := range pools {
		before := poolSet(t, p)
		err := p.Add(mkTx(next))
		if !errors.Is(err, pool.ErrWouldGapNonce) {
			t.Fatalf("node %d next nonce: want ErrWouldGapNonce, got %v", i, err)
		}
		after := poolSet(t, p)
		assertNoNewGap(t, before, after, "same-sender next nonce rejected")
		if len(after) != len(before) {
			t.Fatalf("node %d rejected add changed pool size", i)
		}
		if p.Size() != cap {
			t.Fatalf("node %d size=%d after refusal, want %d", i, p.Size(), cap)
		}
		t.Logf("input: node %d u4 fee900 (next nonce, pool is u's {0,1,2,3}) -> REJECT ErrWouldGapNonce; pool unchanged", i)
	}
	assertPoolsConverged(t, pools, "after same-sender continuation refusal")
	t.Logf("decision: cross-node pressure test complete; concurrent ingress late-errors=%d; no new nonce gap; cap=%d always held", nRej, cap)
}
