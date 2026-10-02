package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestPoolRebaseCrossNodeConsistency drives the same transaction stream into
// every node, packs a block, then checks that after the pool rebases onto the
// new head state all nodes retain the SAME pending set (by hash) and would
// pack the SAME next block (ordered hashes). Admission, capacity eviction and
// rebase are pure functions of genesis + the committed block sequence, so
// honest nodes converge.
func TestPoolRebaseCrossNodeConsistency(t *testing.T) {
	c := newCluster(t, 4)

	// A funded user submits a batch of transactions with deliberately mixed
	// fees/nonces through node C; gossip reaches everyone identically.
	sender := addr(99)
	mk := func(nonce, fee, value uint64) *types.Tx {
		return types.SignTx(&types.Tx{From: sender, To: addr(2), Nonce: nonce, Value: value, Fee: fee})
	}
	// nonces 0..5, varied fees. Submission order is fixed and identical.
	batch := []*types.Tx{
		mk(0, 5, 10),
		mk(1, 9, 10),
		mk(2, 2, 10),
		mk(3, 9, 10),
		mk(4, 1, 10),
		mk(5, 7, 10),
	}
	for _, tx := range batch {
		if err := c.nodes[2].SubmitTx(tx); err != nil {
			t.Fatalf("submit nonce=%d: %v", tx.Nonce, err)
		}
	}
	c.net.DeliverAll()

	assertSamePool := func(stage string) {
		var ref map[types.Hash]bool
		var refOrder []types.Hash
		for i, nd := range c.nodes {
			// Ordered reflects what would actually be packed (executable).
			txs := nd.OrderedExport(100)
			set := map[types.Hash]bool{}
			var order []types.Hash
			for _, tx := range txs {
				h := types.HashBytes(types.CanonicalTx(tx))
				set[h] = true
				order = append(order, h)
			}
			if i == 0 {
				ref, refOrder = set, order
				continue
			}
			if len(set) != len(ref) {
				t.Fatalf("%s: node %s packs %d txs, want %d", stage, nd.Name, len(set), len(ref))
			}
			for h := range ref {
				if !set[h] {
					t.Fatalf("%s: node %s missing tx %x after rebase", stage, nd.Name, h[:8])
				}
			}
			for k := range order {
				if order[k] != refOrder[k] {
					t.Fatalf("%s: node %s pack order diverges at position %d", stage, nd.Name, k)
				}
			}
		}
		t.Logf("decision: %s — all nodes retain identical set and identical pack order (%d txs)",
			stage, len(refOrder))
	}
	assertSamePool("pre-block")

	// Produce a block: nonces execute in nonce order regardless of fees
	// (a sender's sequence must be contiguous), then every node rebases.
	c.step()
	c.assertSameHead()
	assertSamePool("after-rebase-h1")

	// The executed nonces (0..N) are gone everywhere; remaining pending sets
	// still match and remain executable-contiguous.
	c.step()
	c.assertSameHead()
	assertSamePool("after-rebase-h2")
}
