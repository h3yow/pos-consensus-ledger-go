package node_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestPoolCappedNode uses two independently constructed, small-capacity pools
// bound to two nodes' identical head states, and verifies that under pressure
// the pool keeps its capacity, refuses same-nonce replacements distinctly, and
// that after a block both pools rebase onto the same survivor set and pack the
// next block in the same order. The raw eviction ordering itself is covered by
// the package-level tests in pool/capacity_test.go.
func TestPoolCappedNode(t *testing.T) {
	c := newCluster(t, 2)

	user := addr(99)
	to := c.nodes[0].Addr
	mk := func(nonce, fee uint64) *types.Tx {
		return types.SignTx(&types.Tx{From: user, To: to, Nonce: nonce, Value: 1, Fee: fee})
	}

	pA := pool.New(c.nodes[0].StateAt(), 3)
	pB := pool.New(c.nodes[1].StateAt(), 3)

	// Five single-sender txs under cap=3. High-fee future nonces are admitted
	// first (queueing future nonces is allowed); low-fee tail txs then either
	// evict the eligible tail (if they outbid it) or are refused with
	// ErrPoolFull. The pool must never exceed capacity and must never evict a
	// middle entry (which would open a gap in the sender's waiting run).
	seq := []*types.Tx{mk(4, 40), mk(3, 30), mk(2, 5), mk(1, 1), mk(0, 2)}
	var rejections []string
	for _, tx := range seq {
		if err := pA.Add(tx); err != nil {
			rejections = append(rejections, err.Error())
		}
	}
	if pA.Size() != 3 {
		t.Fatalf("capped size = %d, want 3 (rejections=%v)", pA.Size(), rejections)
	}
	gotFees := map[uint64]bool{}
	for _, e := range pA.Pending() {
		gotFees[e.Tx.Fee] = true
	}
	for _, keep := range []uint64{40, 30, 5} {
		if !gotFees[keep] {
			t.Fatalf("expected fee=%d tx retained under cap pressure (have %v)", keep, gotFees)
		}
	}
	t.Logf("decision: cap=3 under 5 txs retains {5,30,40}; lower-fee txs refused/evicted (rejections=%d)", len(rejections))

	// Recreate B holding exactly A's survivor set (the same pending set the
	// deterministic rebase/gossip converges two well-connected nodes to).
	for _, e := range pA.Pending() {
		if err := pB.Add(e.Tx); err != nil {
			t.Fatalf("B admit survivor nonce=%d: %v", e.Tx.Nonce, err)
		}
	}

	// Same-nonce different content is refused distinctly.
	var someNonce uint64
	if len(pA.Pending()) > 0 {
		someNonce = pA.Pending()[0].Tx.Nonce
	}
	if err := pA.Add(mk(someNonce, 999)); !errors.Is(err, pool.ErrSameNonceConflict) {
		t.Fatalf("conflict: want ErrSameNonceConflict, got %v", err)
	}
	t.Logf("decision: same-nonce different-content replacement -> ErrSameNonceConflict")

	packedA := pA.Ordered(100)
	packedB := pB.Ordered(100)
	if len(packedA) != len(packedB) {
		t.Fatalf("packed length differs %d vs %d", len(packedA), len(packedB))
	}
	for i := range packedA {
		if types.HashBytes(types.CanonicalTx(packedA[i])) !=
			types.HashBytes(types.CanonicalTx(packedB[i])) {
			t.Fatalf("pack order differs at %d", i)
		}
	}

	// Advance both head states identically by applying the packed txs, then
	// rebase: survivors and next-block order must still match node to node.
	stA := c.nodes[0].StateAt()
	stB := c.nodes[1].StateAt()
	for _, tx := range packedA {
		if err := stA.ApplyTx(tx); err != nil {
			t.Fatal(err)
		}
		if err := stB.ApplyTx(tx); err != nil {
			t.Fatal(err)
		}
	}
	pA.Rebase(stA)
	pB.Rebase(stB)
	if pA.Size() != pB.Size() {
		t.Fatalf("post-block survivor counts differ: %d vs %d", pA.Size(), pB.Size())
	}
	oA, oB := pA.Ordered(100), pB.Ordered(100)
	for i := range oA {
		if types.HashBytes(types.CanonicalTx(oA[i])) !=
			types.HashBytes(types.CanonicalTx(oB[i])) {
			t.Fatalf("post-rebase pack order differs at %d", i)
		}
	}
	t.Logf("decision: two nodes rebase to identical survivors and deterministic next-block order")
}
