package node

import (
	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// ProduceBlockOn builds a valid block on an explicit parent node, executing
// the given transactions against a clone of the parent state. It is the
// building block for Byzantine simulations (equivocating proposers, forks)
// in tests: the returned block is fully well-formed and carries the same
// justification as a normal proposal. It is NOT self-applied.
func (n *Node) ProduceBlockOn(parentHash types.Hash, txs []*types.Tx, evs []*types.Evidence) *types.Block {
	parent := n.chain.GetBlock(parentHash)
	if parent == nil {
		return nil
	}
	height := parent.Block.Height + 1
	if n.set.ProposerAt(height) != n.Addr {
		return nil
	}
	st := n.chain.StateOf(parent).Clone()
	for _, ev := range evs {
		bal := st.GetBalance(ev.Offender)
		st.Slash(ev.Offender, bal/3)
	}
	executed := make([]*types.Tx, 0, len(txs))
	for _, t := range txs {
		if err := st.ApplyTx(t); err == nil {
			executed = append(executed, t)
		}
	}
	b := &types.Block{
		Height:       height,
		Proposer:     n.Addr,
		PrevHash:     parentHash,
		StateRoot:    st.Root(),
		Txs:          executed,
		Evidence:     evs,
		JustifyVotes: n.chain.Votes().VotesAt(parent.Block.Height),
	}
	// The twin shares the proposer's key, so the competing proposal carries a
	// genuine signature: this is exactly what makes double-propose evidence
	// verifiable — an outsider cannot build one.
	identity.SignBlock(b, n.priv)
	return b
}

// GossipBlock sends a block to the network without self-application.
func (n *Node) GossipBlock(b *types.Block, recipients ...string) {
	m := simnet.Message{Kind: simnet.MsgBlock, From: n.Name, Block: b}
	if len(recipients) == 0 {
		n.net.Gossip(m)
	} else {
		n.net.GossipTo(m, recipients...)
	}
}

// VotesAt exposes the vote pool contents at a height (test helper).
func (n *Node) VotesAt(h uint64) []*types.Vote {
	return n.chain.Votes().VotesAt(h)
}

// OrderedExport returns the transactions this node would currently pack, in
// deterministic order (test helper).
func (n *Node) OrderedExport(limit int) []*types.Tx {
	return n.pool.Ordered(limit)
}

// PoolSizeExport reports the current pending count (test helper).
func (n *Node) PoolSizeExport() int { return n.pool.Size() }

// AddBlockDirect feeds an already-built block straight into this node's
// chain and returns the validation error (test helper).
func (n *Node) AddBlockDirect(b *types.Block) error {
	_, err := n.chain.AddBlock(b)
	return err
}

// PendingEvidence returns a copy of queued slashing evidence (test helper).
func (n *Node) PendingEvidence() []*types.Evidence {
	out := make([]*types.Evidence, len(n.pending))
	copy(out, n.pending)
	return out
}

// ProposedAt returns the block hash this node first saw for height h.
func (n *Node) ProposedAt(h uint64) (types.Hash, bool) {
	x, ok := n.proposed[h]
	return x, ok
}
