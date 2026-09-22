package node_test

import (
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// sumGenesis computes total genesis balances (stakes + user account).
func sumGenesis(c *cluster) uint64 {
	var total uint64
	for _, v := range c.vals {
		total += v.Stake
	}
	total += c.gs.GetBalance(addr(99))
	return total
}

func blkBlock(b *types.Block) types.Hash { return types.HashBlock(b) }
