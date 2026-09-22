package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestConcurrentPropose models a round in which every validator tries to
// propose the same height concurrently (no leader locking, purely the
// deterministic schedule decides). Exactly one block may be valid:
//   - the scheduled proposer's block is accepted everywhere;
//   - blocks "produced" by non-scheduled validators are rejected with
//     ErrWrongProposer on every node;
//   - after votes the chain converges and advances.
func TestConcurrentPropose(t *testing.T) {
	c := newCluster(t, 4)
	c.step() // h=1

	// Reach h=2 by a normal step to prime parent; then test h=3 contention.
	c.step() // h=2
	target := uint64(3)
	scheduled := c.proposerIndex(target)

	// Every node attempts to build a block at target height. Only the
	// scheduled one returns non-nil through ProduceBlock.
	var winner *types.Block
	for i, nd := range c.nodes {
		b := nd.ProduceBlock()
		if i == scheduled {
			if b == nil {
				t.Fatalf("scheduled node %s did not produce", nd.Name)
			}
			winner = b
		} else if b != nil {
			t.Fatalf("non-scheduled node %s produced a block", nd.Name)
		}
	}
	c.net.DeliverAll()
	for _, nd := range c.nodes {
		if types.HashBlock(nd.Head()) != types.HashBlock(winner) {
			t.Fatalf("node %s did not accept the unique scheduled block", nd.Name)
		}
	}
	wh := types.HashBlock(winner)
	t.Logf("h=%d unique proposal by %s hash=%x accepted on all nodes",
		target, c.nodes[scheduled].Name, wh[:8])

	// A block falsely claiming another proposer must be rejected identically.
	forged := *winner
	forged.Proposer = c.nodes[(scheduled+1)%len(c.nodes)].Addr
	forged.StateRoot = winner.StateRoot
	for _, nd := range c.nodes {
		err := nd.AddBlockDirect(&forged)
		if err == nil {
			t.Fatalf("node %s accepted block with wrong proposer", nd.Name)
		}
	}
	t.Logf("decision: blocks with non-scheduled proposer rejected on all nodes")

	// Finalize and continue: convergence holds over several rounds.
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()
	for h := uint64(4); h <= 6; h++ {
		c.step()
	}
	c.assertSameHead()
	if c.nodes[0].FinalizedHeight() < target {
		t.Fatalf("expected finality at/above %d, got %d", target, c.nodes[0].FinalizedHeight())
	}
	t.Logf("converged head h=%d finality=%d",
		c.nodes[0].Head().Height, c.nodes[0].FinalizedHeight())
}
