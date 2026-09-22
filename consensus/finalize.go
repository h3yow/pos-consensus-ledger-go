package consensus

import "github.com/h3yow/pos-consensus-ledger-go/types"

// quorum is the minimum stake weight for finality: floor(2*total/3)+1,
// i.e. strictly more than two thirds of total stake.
func quorum(totalStake uint64) uint64 {
	return (2*totalStake)/3 + 1
}

// weightFor returns the effective stake of a voter for a target block.
// Voting weight equals the voter's live balance in the target block's
// post-state, so slashed-away stake cannot contribute to finality.
func (c *Chain) weightFor(target types.Hash, voter types.Address) uint64 {
	if n := c.nodes[target]; n != nil {
		return n.state.GetBalance(voter)
	}
	return c.set.Stake(voter)
}

// justified reports whether the block at (hash,height) carries a >2/3 quorum
// of distinct validator votes, weighted by live stake.
func (c *Chain) justified(hash types.Hash, height uint64) bool {
	total := uint64(0)
	if n := c.nodes[hash]; n != nil {
		for _, v := range c.set.Addrs() {
			total += n.state.GetBalance(v)
		}
	} else {
		total = c.set.TotalStake()
	}
	if total == 0 {
		return false
	}
	// Distinct voters only; each address counted once per height.
	seen := map[types.Address]bool{}
	power := uint64(0)
	for _, v := range c.votes.VotesAt(height) {
		if v.BlockHash != hash || seen[v.Voter] {
			continue
		}
		seen[v.Voter] = true
		power += c.weightFor(hash, v.Voter)
	}
	return power >= quorum(total)
}

// refreshFinalization walks the canonical chain upward from head and marks
// the highest justified block finalized. Finality is monotonic: it can never
// move backwards, which is the fork-choice protection against rollback.
func (c *Chain) refreshFinalization() {
	cur := c.chooseHead()
	if cur == nil {
		return
	}
	// Update justification flags on stored nodes.
	for _, n := range c.nodes {
		n.justified = (n == c.genesis) || c.justified(n.Hash, n.Block.Height)
	}
	// Find the highest justified ancestor of head at or above current finality.
	var highest *BlockNode
	for x := cur; x != nil; x = x.Parent {
		if x.Block.Height < c.finalized.Block.Height {
			break
		}
		if x.justified {
			highest = x
			break
		}
	}
	if highest != nil && highest.Block.Height >= c.finalized.Block.Height {
		// Only advance; never regress.
		if highest.Block.Height > c.finalized.Block.Height || highest.Hash == c.finalized.Hash {
			if highest.Block.Height > c.finalized.Block.Height {
				c.finalized = highest
			}
		}
		for x := highest; x != nil; x = x.Parent {
			x.finalized = true
		}
	}
}

// CheckFinalization forces a re-evaluation and reports whether n is finalized.
func (c *Chain) CheckFinalization(n *BlockNode) bool {
	c.refreshFinalization()
	return n != nil && n.finalized
}

// QuorumExport exposes quorum for tests.
func QuorumExport(total uint64) uint64 { return quorum(total) }
