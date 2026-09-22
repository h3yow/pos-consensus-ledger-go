// Command simnode runs an in-process multi-validator PoS demonstration:
//
//  1. four validators on a simulated network build blocks and vote to
//     finality (strictly more than 2/3 stake), proving canonical-chain
//     convergence and identical state roots on every node;
//  2. a byzantine validator double-signs conflicting votes; the evidence
//     is gossiped, included in a block, and the offender is slashed with
//     total balance conserved.
//
// All delivery is explicit (cluster.Drain), so nothing depends on
// wall-clock timing or real network sockets.
package main

import (
	"fmt"
	"os"

	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

func main() {
	fmt.Println("=== PoS in-process multi-node demo ===")
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Weights: 10/20/30/40 -> total 100; threshold = floor(100*2/3)+1 = 67.
	c, err := testkit.NewCluster([]uint64{10, 20, 30, 40}, true)
	if err != nil {
		return err
	}
	defer fmt.Println("=== demo finished ===")

	total0 := c.Nodes[0].HeadState().TotalBalance()
	fmt.Printf("initial total balance: %d\n", total0)

	// Drive 6 heights: round-robin proposers, explicit message delivery.
	// Before each height, node 2 submits the next in-nonce transaction;
	// fees differ so the mempool packing rule (fee desc) is exercised by
	// the additional out-of-order-free traffic in the tests.
	fees := []uint64{2, 4, 3, 5, 1, 6}
	for height := uint64(1); height <= 6; height++ {
		i := int(height - 1)
		tx := testkit.SignTx(c.Keys[1], c.Addr(3), uint64(i), 5, fees[i])
		if err := c.Nodes[1].SubmitTx(tx); err != nil {
			return fmt.Errorf("submit h=%d: %w", height, err)
		}
		c.Drain()
		if _, err := c.DriveNext(0); err != nil {
			return fmt.Errorf("height %d propose: %w", height, err)
		}
		c.Drain()
	}

	// Assert heads, finality and state-root agreement.
	headHash := c.Nodes[0].Head().Hash()
	headRoot := c.Nodes[0].Head().StateRoot
	headHeight := c.Nodes[0].Head().Height
	for i, n := range c.Nodes {
		if n.Head().Hash() != headHash {
			return fmt.Errorf("node %s head %s != %s", n.ID(), n.Head().Hash(), headHash)
		}
		if n.Head().StateRoot != headRoot {
			return fmt.Errorf("node %s state root mismatch", n.ID())
		}
		fh, fht := n.Finalized()
		fmt.Printf("node %s: head h=%d hash=%s root=%s finalized h=%d %s\n",
			n.ID(), n.Head().Height, headHash, headRoot, fht, fh)
		_ = i
	}

	// Byzantine: validator index 0 double-signs two different blocks at
	// the same height/round. Use two distinct existing block hashes as
	// vote targets to produce conflicting signed votes.
	b1 := c.Nodes[0].Head()
	var target2 types.Hash
	if chain, err := c.Nodes[0].Store().Chain(headHash); err == nil && len(chain) > 2 {
		target2 = chain[len(chain)-2].Hash()
	}
	byz := c.Nodes[0]
	v1 := byz.ForceVote(headHeight+1, 0, b1.Hash())
	v2 := byz.ForceVote(headHeight+1, 0, target2)
	fmt.Printf("byzantine double-sign: h=%d r=0 blocks=%s vs %s\n",
		headHeight+1, v1.BlockHash, v2.BlockHash)
	c.Drain()

	// Produce two more heights so the evidence is packed into a block.
	for k := 0; k < 2; k++ {
		if _, err := c.DriveNext(0); err != nil {
			return fmt.Errorf("post-evidence propose: %w", err)
		}
		c.Drain()
	}

	offender := c.Addr(0)
	total1 := c.Nodes[0].HeadState().TotalBalance()
	fmt.Printf("post-slash balances -> offender=%d treasury=%d total=%d (conserved=%v)\n",
		c.Nodes[0].HeadState().Balance(offender),
		c.Nodes[0].HeadState().Balance(c.Genesis.Treasury),
		total1, total1 == total0)
	if total1 != total0 {
		return fmt.Errorf("total balance not conserved: %d != %d", total1, total0)
	}
	return nil
}
