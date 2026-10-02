package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// setupEquivocation drives the same fork + double-vote scenario used in the
// slashing test and returns once the evidence is queued on every node (head
// h=2, offender D has voted twice; proposer C has double-proposed).
func setupEquivocation(t *testing.T, c *cluster) (winner, loser types.Hash, offender types.Address, b1, b2a, b2b *types.Block) {
	b1 = c.step()

	p2 := c.proposerIndex(2)
	twin := c.twin(p2, "E")
	c.net.SendTo("E", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.DeliverAll()
	for _, v := range c.nodes[0].VotesAt(1) {
		c.net.SendTo("E", simnet.Message{Kind: simnet.MsgVote, From: "S", Vote: v})
	}
	c.net.DeliverAll()

	b2a = c.nodes[p2].ProduceBlock()
	for _, nd := range c.nodes {
		for c.net.Pending(nd.Name) > 0 {
			c.net.Drop(nd.Name, 0)
		}
	}
	tx := types.SignTx(&types.Tx{From: addr(99), To: addr(1), Nonce: 0, Value: 1, Fee: 1})
	b2b = twin.nd.ProduceBlockOn(types.HashBlock(b1), []*types.Tx{tx}, nil)
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
	ha, hb := types.HashBlock(b2a), types.HashBlock(b2b)

	winner = c.assertSameHead()
	loser = ha
	if winner == ha {
		loser = hb
	}
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()

	offender = c.nodes[3].Addr // validator D
	dTwin := newNode(c, "D2", offender)
	for _, blk := range []*types.Block{b1, b2a, b2b} {
		c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: blk})
	}
	c.net.DeliverAll()
	vWin := signVoteAs(offender, 2, winner)
	vLose := signVoteAs(offender, 2, loser)
	for _, nd := range c.nodes {
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgVote, From: "BYZ", Vote: vWin})
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgVote, From: "BYZ", Vote: vLose})
	}
	c.net.DeliverAll()
	_ = dTwin
	return winner, loser, offender, b1, b2a, b2b
}

// TestSlashFundsTreasuryConservation: once evidence lands on chain, the
// offenders' stake is credited to the protocol public treasury (not burned),
// every node holds the identical treasury balance and state root, and the
// global invariant live(incl. treasury)+feeBurn == genesis total holds at
// every node.
func TestSlashFundsTreasuryConservation(t *testing.T) {
	c := newCluster(t, 4)
	genesisTotal := sumGenesis(c)
	setupEquivocation(t, c)

	// Advance until evidence is included canonically.
	included := false
	for h := uint64(3); h <= 14 && !included; h++ {
		blk := c.step()
		if len(blk.Evidence) > 0 {
			included = true
		}
	}
	if !included {
		t.Fatal("evidence never included on canonical chain")
	}
	c.assertSameHead()

	// Treasury funded and identical across nodes.
	var treasury uint64
	var root types.Hash
	for i, nd := range c.nodes {
		st := nd.StateAt()
		if i == 0 {
			treasury, root = st.TreasuryBalance(), nd.Head().StateRoot
		} else {
			if st.TreasuryBalance() != treasury {
				t.Fatalf("node %s treasury %d != %d", nd.Name, st.TreasuryBalance(), treasury)
			}
			if nd.Head().StateRoot != root {
				t.Fatalf("node %s state root mismatch after slash", nd.Name)
			}
		}
		// Global conservation: treasury is part of live balance; only fees burn.
		live := st.TotalBalance()
		burn := nd.Chain().BurnOn(nd.Chain().Head())
		if live+burn != genesisTotal {
			t.Fatalf("node %s conservation: live=%d burn=%d total=%d",
				nd.Name, live, burn, genesisTotal)
		}
	}
	if treasury == 0 {
		t.Fatal("treasury must be funded by slashing")
	}
	t.Logf("decision: treasury=%d identical on all nodes; root=%x; live+feeBurn=%d conserved",
		treasury, root[:8], genesisTotal)
}

// TestSlashReducesVotingWeight verifies the offender's effective voting
// weight tracks its remaining live balance after slashing: the justification
// power its votes contribute drops with the stake moved to the treasury.
func TestSlashReducesVotingWeight(t *testing.T) {
	c := newCluster(t, 4)
	winner, loser, offender, _, _, _ := setupEquivocation(t, c)
	_ = loser

	chain0 := c.nodes[0].Chain()
	head := chain0.Head()

	// Before any slash at h=3, a vote by D for h=2 carries D's full balance.
	preVote := signVoteAs(offender, 2, winner)
	parent := head // canonical h=2 before the slashing block
	// Simulate: build a throwaway block referencing parent to measure power
	// via JustificationPower against the h=2 post-state.
	probe := &types.Block{Height: 3, Proposer: c.nodes[0].Addr, PrevHash: parent.Hash,
		JustifyVotes: []*types.Vote{preVote}}
	prePower := chain0.JustificationPower(probe, parent)
	preBal := chain0.StateOf(parent).GetBalance(offender)
	if prePower != preBal {
		t.Fatalf("pre-slash weight = %d want balance %d", prePower, preBal)
	}

	// Include the slash on chain.
	included := false
	for h := uint64(3); h <= 14 && !included; h++ {
		blk := c.step()
		if len(blk.Evidence) > 0 {
			included = true
		}
	}
	if !included {
		t.Fatal("evidence never included")
	}
	c.assertSameHead()

	// After slashing, D's balance and thus its voting weight are reduced.
	postBal := c.nodes[0].StateAt().GetBalance(offender)
	if postBal >= preBal {
		t.Fatalf("offender balance did not drop: pre=%d post=%d", preBal, postBal)
	}
	// Weight at the slashed block for a subsequent height uses the new state:
	// D alone must contribute exactly its reduced balance.
	slashedHead := c.nodes[0].Chain().Head()
	postVote := signVoteAs(offender, slashedHead.Block.Height, types.HashBlock(slashedHead.Block))
	next := &types.Block{
		Height:       slashedHead.Block.Height + 1,
		Proposer:     c.nodes[0].Addr,
		PrevHash:     types.HashBlock(slashedHead.Block),
		JustifyVotes: []*types.Vote{postVote},
	}
	postPower := c.nodes[0].Chain().JustificationPower(next, slashedHead)
	if postPower != postBal {
		t.Fatalf("post-slash weight = %d want reduced balance %d", postPower, postBal)
	}
	t.Logf("decision: offender voting weight tracked balance %d -> %d (treasury holds the difference)",
		preBal, postBal)
}

var _ = state.PublicTreasury
var _ = consensus.NewSet
