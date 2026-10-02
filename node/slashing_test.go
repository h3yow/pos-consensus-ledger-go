package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestDoubleVoteSlashing: after the canonical h=2 block is chosen, a
// validator casts two different votes at the same height (one for the
// canonical block, one for an abandoned fork). Equivocation is detected,
// evidence embedded in a later block, 1/3 of stake burned identically on
// every node, and balance + burn == genesis total (conservation).
func TestDoubleVoteSlashing(t *testing.T) {
	c := newCluster(t, 4)
	b1 := c.step()

	// Create an h=2 fork via a Byzantine twin of the scheduled proposer.
	p2 := c.proposerIndex(2)
	twin := c.twin(p2, "E")
	c.net.SendTo("E", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.DeliverAll()
	for _, v := range c.nodes[0].VotesAt(1) {
		c.net.SendTo("E", simnet.Message{Kind: simnet.MsgVote, From: "S", Vote: v})
	}
	c.net.DeliverAll()

	b2a := c.nodes[p2].ProduceBlock()
	for _, nd := range c.nodes {
		for c.net.Pending(nd.Name) > 0 {
			c.net.Drop(nd.Name, 0)
		}
	}
	tx := types.SignTx(&types.Tx{From: addr(99), To: addr(1), Nonce: 0, Value: 1, Fee: 1})
	b2b := twin.nd.ProduceBlockOn(types.HashBlock(b1), []*types.Tx{tx}, nil)
	// Simulate delayed arrival: C,D receive b2b first; A,B receive b2a first.
	// Every node ultimately receives both, but arrival order differs.
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.DeliverAll()
	// Late arrival of the competing block on each side.
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.DeliverAll()
	ha, hb := types.HashBlock(b2a), types.HashBlock(b2b)

	// Everyone converges on the deterministic winner and votes it to quorum,
	// so h=2 becomes part of the justifiable canonical chain.
	winner := c.assertSameHead()
	loser := ha
	if winner == ha {
		loser = hb
	}
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()

	// Offender = validator D (index 3). It already voted for the winner via
	// the honest round above only if its head was winner — CastVote guards one
	// vote per height, so instead perform the equivocation directly at gossip
	// level BEFORE its honest vote reaches others. To keep the test robust we
	// pick a validator that has NOT yet voted at h=2: reset its pool is not
	// exposed, so we use a fresh identity process with validator D's key.
	offender := c.nodes[3].Addr
	dTwin := newNode(c, "D2", offender)
	// Feed both fork blocks to D's twin so both vote targets are known.
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b2a})
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b2b})
	c.net.DeliverAll()

	// D equivocates: two votes, same height, different block hashes. Both
	// carry signatures made with D's own key (the only way evidence can be
	// valid); no other party could have produced them.
	vWin := signVoteAs(offender, 2, winner)
	vLose := signVoteAs(offender, 2, loser)
	for _, nd := range c.nodes {
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgVote, From: "BYZ", Vote: vWin})
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgVote, From: "BYZ", Vote: vLose})
	}
	c.net.DeliverAll()
	_ = dTwin

	// Every honest node must collect the double-vote evidence.
	for _, nd := range c.nodes {
		found := false
		for _, ev := range nd.PendingEvidence() {
			if ev.Kind == "vote" && ev.Offender == offender && ev.Height == 2 {
				found = true
			}
		}
		if !found {
			t.Fatalf("node %s did not collect double-vote evidence", nd.Name)
		}
	}
	t.Logf("decision: double-vote by %x at h=2 recorded on all nodes", offender[:8])

	genesisTotal := sumGenesis(c)
	pre := c.nodes[0].StateAt().GetBalance(offender)

	// Advance until the vote evidence is included on the canonical chain.
	included := false
	for h := uint64(3); h <= 14 && !included; h++ {
		blk := c.step()
		for _, ev := range blk.Evidence {
			t.Logf("height %d block contains evidence kind=%s offender=%x",
				h, ev.Kind, ev.Offender[:8])
			if ev.Kind == "vote" && ev.Offender == offender {
				included = true
			}
		}
	}
	if !included {
		t.Fatal("double-vote evidence never included on canonical chain")
	}
	c.assertSameHead()

	// Conservation on every node and exact 1/3 slash. Slashed stake now lives
	// in the protocol public treasury, so TotalBalance (which includes it)
	// plus the fee burn still equals the genesis total.
	for _, nd := range c.nodes {
		live := nd.StateAt().TotalBalance()
		burn := nd.Chain().BurnOn(nd.Chain().Head())
		if live+burn != genesisTotal {
			t.Fatalf("node %s conservation violated: live=%d burn=%d total=%d",
				nd.Name, live, burn, genesisTotal)
		}
	}
	post := c.nodes[0].StateAt().GetBalance(offender)
	if post != pre-pre/3 {
		t.Fatalf("expected slash of 1/3: pre=%d post=%d want=%d", pre, post, pre-pre/3)
	}
	treasury := c.nodes[0].StateAt().TreasuryBalance()
	// The canonical h=3 block contains two distinct offences:
	//   - double propose by the scheduled h=2 proposer (proposerOffender)
	//   - double vote by D (offender)
	// Each costs 1/3 of the offender's own balance, and every slashed coin
	// lands in the single protocol treasury.
	proposerOffender := b2a.Proposer
	// The proposer may already have received a value transfer in the winning
	// h=2 block, so its pre-slash balance is read from the parent (h=2) state
	// rather than assumed to be the genesis 1000.
	head := c.nodes[0].Chain().Head()
	proPre := c.nodes[0].Chain().StateOf(head.Parent).GetBalance(proposerOffender)
	proPost := c.nodes[0].StateAt().GetBalance(proposerOffender)
	if proPost != proPre-proPre/3 {
		t.Fatalf("proposer slash: pre=%d post=%d want=%d", proPre, proPost, proPre-proPre/3)
	}
	wantTreasury := (pre - post) + (proPre - proPost)
	if treasury != wantTreasury {
		t.Fatalf("treasury should hold %d (voter %d + proposer %d), got %d",
			wantTreasury, pre-post, proPre-proPost, treasury)
	}
	t.Logf("slashed proposer %x %d -> %d and voter %x %d -> %d; treasury=%d; conservation holds on all nodes",
		proposerOffender[:8], proPre, proPost, offender[:8], pre, post, treasury)

	// Forged/ill-formed evidence must never be accepted by any node's set.
	set := consensus.NewSet(c.vals)
	if err := set.VerifyEvidenceExport(&types.Evidence{
		Kind: "vote", Offender: addr(3), Height: 9,
		A: []byte("junk"), B: []byte("jnuk"),
	}); err == nil {
		t.Fatal("forged evidence should fail verification")
	}
}
