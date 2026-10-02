package node_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// twin creates a second process sharing an existing validator's identity.
// It independently executes blocks, so it can simulate an equivocating
// proposer producing a distinct block at the same height.
func (c *cluster) twin(ofIndex int, name string) *nodeRef {
	nd := newNode(c, name, c.nodes[ofIndex].Addr)
	return &nodeRef{nd: nd, name: name}
}

// TestForkConvergence: a Byzantine proposer double-proposes height 2, the two
// blocks reach disjoint recipient sets (simulated partition/delay), and one
// arrives late. Deterministic fork choice must still converge every node to
// one head; duplicate broadcasts must be harmless; and after finality a block
// extending the abandoned fork must be refused.
func TestForkConvergence(t *testing.T) {
	c := newCluster(t, 4)

	// Height 1 normal.
	b1 := c.step()

	// Register the proposer's Byzantine twin after h1 and sync it.
	p2 := c.proposerIndex(2)
	twin := c.twin(p2, "E")
	c.net.SendTo("E", simnet.Message{Kind: simnet.MsgBlock, From: "SYNC", Block: b1})
	c.net.DeliverAll()
	// Forward all h1 votes to the twin so it can justify h2.
	for _, v := range c.nodes[0].Chain().Votes().VotesAt(1) {
		c.net.SendTo("E", simnet.Message{Kind: simnet.MsgVote, From: "SYNC", Vote: v})
	}
	c.net.DeliverAll()

	// Honest proposer builds b2a with no txs; twin builds b2b with a tx.
	b2a := c.nodes[p2].ProduceBlock()
	if b2a == nil {
		t.Fatal("no h2 block")
	}
	// Hold back b2a: clear all queues except proposer self-application.
	for _, nd := range c.nodes {
		for c.net.Pending(nd.Name) > 0 {
			c.net.Drop(nd.Name, 0)
		}
	}
	tx := types.SignTx(&types.Tx{From: addr(99), To: addr(1), Nonce: 0, Value: 1, Fee: 1})
	b2b := twin.nd.ProduceBlockOn(types.HashBlock(b1), []*types.Tx{tx}, nil)
	if b2b == nil {
		t.Fatal("twin failed to build competing block")
	}
	if types.HashBlock(b2a) == types.HashBlock(b2b) {
		t.Fatal("competing blocks must differ")
	}

	// Delayed arrival: C,D see b2b first, A,B see b2a first; everyone
	// eventually receives both.
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.DeliverAll()
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.DeliverAll()

	// Duplicate broadcasts of each must be tolerated.
	for _, nd := range c.nodes {
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgBlock, From: "DUP", Block: b2a})
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgBlock, From: "DUP", Block: b2b})
	}
	c.net.DeliverAll()

	// Every node stores both blocks and selects the same winner.
	for _, nd := range c.nodes {
		if nd.Chain().GetBlock(types.HashBlock(b2a)) == nil ||
			nd.Chain().GetBlock(types.HashBlock(b2b)) == nil {
			t.Fatalf("node %s missing a fork block", nd.Name)
		}
	}
	winner := c.assertSameHead()
	ha, hb := types.HashBlock(b2a), types.HashBlock(b2b)
	t.Logf("fork h=2 winner=%x (a=%x b=%x)", winner[:8], ha[:8], hb[:8])

	// Vote winner to finality and advance; finality must not regress.
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()
	b3 := c.step()
	if c.nodes[0].FinalizedHeight() < 2 {
		t.Fatalf("expected h2 finalized, got %d", c.nodes[0].FinalizedHeight())
	}

	// A late block extending the loser fork must be rejected specifically as
	// conflicting with finalized history (ErrFinalizedReorg). Give it a valid
	// >2/3 justification (as an attacker with network control could gather) so
	// that finality protection, not an earlier check, is what stops it.
	loser := b2a
	if winner == ha {
		loser = b2b
	}
	conflict := twin.nd.ProduceBlockOn(types.HashBlock(loser), nil, nil)
	if conflict == nil {
		t.Fatal("could not build conflicting block on loser")
	}
	conflict.JustifyVotes = nil // height-3 block needs justification for loser
	for i := 0; i < 4; i++ {
		v := c.signVote(&types.Vote{
			Height:    loser.Height,
			Voter:     c.nodes[i].Addr,
			BlockHash: types.HashBlock(loser),
		})
		conflict.JustifyVotes = append(conflict.JustifyVotes, v)
	}
	// JustifyVotes are part of the signed canonical block: re-sign with the
	// (Byzantine) proposer's own key after assembling them.
	types.SignBlock(conflict, twin.nd.PrivKey())
	err := c.nodes[0].AddBlockDirect(conflict)
	if !errors.Is(err, consensus.ErrFinalizedReorg) {
		t.Fatalf("expected ErrFinalizedReorg, got %v", err)
	}
	t.Logf("decision: justified late fork block rejected by finality (%v)", err)

	// Everyone remains on the same canonical chain after the attack.
	c.assertSameHead()
	if types.HashBlock(c.nodes[0].Head()) != types.HashBlock(b3) {
		t.Fatal("canonical head changed after finality")
	}
}
