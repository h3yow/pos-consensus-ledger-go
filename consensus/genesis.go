package consensus

import (
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// GenesisAlloc pre-funds one account.
type GenesisAlloc struct {
	Addr    types.Address
	Balance uint64
}

// MakeGenesis builds the height-0 block, its state commitment and the state
// itself. Validators must also appear in allocations (stake weight for voting
// equals live balance; the proposer schedule uses the genesis layout).
func MakeGenesis(vals []*types.Validator, allocs []GenesisAlloc) (*types.Block, *state.State) {
	st := state.New()
	for _, a := range allocs {
		st.SetBalance(a.Addr, a.Balance)
	}
	g := &types.Block{
		Height:    0,
		Proposer:  types.Address{},
		StateRoot: st.Root(),
	}
	return g, st
}

// NewChainWithSet creates a chain bound to a validator set.
func NewChainWithSet(vals []*types.Validator, genesis *types.Block, genesisState *state.State) *Chain {
	c := NewChain(genesis, genesisState)
	c.set = NewSet(vals)
	c.votes.bindSet(c.set)
	return c
}
