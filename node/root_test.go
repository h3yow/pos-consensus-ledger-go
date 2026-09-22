package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestStateRootConsensus drives several blocks with transactions and checks
// that every node computes the identical state root at every height.
func TestStateRootConsensus(t *testing.T) {
	c := newCluster(t, 4)

	// User sends two transactions (nonce 0,1) submitted at node C; they
	// propagate through the simulated network.
	send := func(nonce uint64, fee uint64) {
		tx := types.SignTx(&types.Tx{
			From: addr(99), To: addr(2), Nonce: nonce, Value: 10, Fee: fee, GasLimit: 21000,
		})
		if err := c.nodes[2].SubmitTx(tx); err != nil {
			t.Fatalf("submit: %v", err)
		}
		c.net.DeliverAll()
	}
	send(0, 5)
	send(1, 9)

	rootsByHeight := map[uint64]types.Hash{}
	for h := uint64(1); h <= 5; h++ {
		c.step()
		hh := c.assertSameHead()
		for i, nd := range c.nodes {
			r := nd.Head().StateRoot
			if i == 0 {
				rootsByHeight[h] = r
			} else if rootsByHeight[h] != r {
				t.Fatalf("height %d state root mismatch on node %s", h, nd.Name)
			}
		}
		rr := rootsByHeight[h]
		t.Logf("height %d head=%x root=%x finality=%d", h, hh[:8], rr[:8], c.nodes[0].FinalizedHeight())
	}

	// Balances must have moved: validator 2 received 20 (and fees burned).
	bal := c.nodes[0].StateAt().GetBalance(addr(2))
	if bal <= 1000 {
		t.Fatalf("expected credit to addr(2), got %d", bal)
	}
}
