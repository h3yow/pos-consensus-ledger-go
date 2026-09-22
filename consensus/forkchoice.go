package consensus

import "bytes"

// Fork choice: deterministic scoring over the block tree.
//
// Rules, evaluated strictly in order:
//  1. finalized blocks are immutable; only descendants of the finalized
//     block are eligible (nothing can roll finality back);
//  2. longer chain wins (greatest block height);
//  3. on a height tie, a justified head (carrying a >2/3 quorum) wins;
//  4. on a tie, the lexicographically smallest head block hash wins.
//
// Given the same set of blocks and votes every node computes the same head.
func (c *Chain) chooseHead() *BlockNode {
	candidates := make([]*BlockNode, 0, len(c.nodes))
	for _, n := range c.nodes {
		if c.extendsFinalized(n) {
			candidates = append(candidates, n)
		}
	}
	var best *BlockNode
	for _, n := range candidates {
		if best == nil || c.compareNodes(n, best) > 0 {
			best = n
		}
	}
	return best
}

// compareNodes defines the total order between candidate heads:
// returns 1 if a ranks higher than b, -1 lower, 0 equal.
func (c *Chain) compareNodes(a, b *BlockNode) int {
	if a.Block.Height != b.Block.Height {
		if a.Block.Height > b.Block.Height {
			return 1
		}
		return -1
	}
	if a.justified != b.justified {
		if a.justified {
			return 1
		}
		return -1
	}
	return bytes.Compare(b.Hash[:], a.Hash[:]) // smaller hash wins => invert
}
