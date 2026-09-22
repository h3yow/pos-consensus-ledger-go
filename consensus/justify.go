package consensus

import (
	"bytes"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// JustificationPower validates the JustifyVotes attached to a block and sums
// the voting power they carry for the block's parent.
//
// This is the sole authority for the justification check: validity is a pure
// function of (block, parent state), independent of which votes a particular
// node happened to receive. Every node therefore accepts or rejects the same
// block regardless of message timing.
//
// A justification vote must:
//   - have a valid signature;
//   - be cast by a genesis validator;
//   - target the parent height and parent hash;
//   - come from a distinct voter (duplicate voters count once).
//
// Weight is the voter's balance in the parent post-state, so slashed stake
// cannot contribute to quorum.
func (c *Chain) JustificationPower(b *types.Block, parent *BlockNode) uint64 {
	seen := map[types.Address]bool{}
	var power uint64
	for _, v := range b.JustifyVotes {
		if !types.VerifyVote(v) {
			continue
		}
		if !c.set.Contains(v.Voter) || seen[v.Voter] {
			continue
		}
		if v.Height != parent.Block.Height || !bytes.Equal(v.BlockHash[:], parent.Hash[:]) {
			continue
		}
		seen[v.Voter] = true
		power += parent.state.GetBalance(v.Voter)
	}
	return power
}
