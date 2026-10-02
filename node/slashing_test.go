package node_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
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

	// D equivocates: two votes, same height, different block hashes. Both are
	// signed by D's OWN key (the twin holds it); an attacker without that key
	// could produce neither, which is exactly what the verification tests prove.
	vWin := c.signVote(&types.Vote{Height: 2, Voter: offender, BlockHash: winner})
	vLose := c.signVote(&types.Vote{Height: 2, Voter: offender, BlockHash: loser})
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
	pre := c.gs.GetBalance(offender)

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

	// The h=2 double-PROPOSE and the double-VOTE may target the same validator
	// depending on the (address-sorted) schedule. Evidence executes in the
	// canonical chain's application order; replay every slash of the offender
	// from its genesis balance (heights low->high, evidence list order) to
	// predict the final balance.
	cur := pre
	var anc []*consensus.BlockNode
	for n := c.nodes[0].Chain().Head(); n != nil && n.Block.Height > 0; n = n.Parent {
		anc = append(anc, n)
	}
	for i := len(anc) - 1; i >= 0; i-- {
		var kinds []string
		for _, ev := range anc[i].Block.Evidence {
			if ev.Offender == offender {
				kinds = append(kinds, ev.Kind)
			}
		}
		for range kinds { // execution order: evidence list order
			cur -= cur / 3
		}
		if len(kinds) > 0 {
			t.Logf("decision: %x slashed at h=%d for %v -> running balance %d",
				offender[:8], anc[i].Block.Height, kinds, cur)
		}
	}

	// Conservation on every node and exact 1/3 slash. Slashed stake now lives
	// in the protocol public account, so live total no longer drops: the audit
	// is live(includes protocol account)+burned fees == genesis total.
	for _, nd := range c.nodes {
		live := nd.StateAt().TotalBalance()
		feeBurn := nd.Chain().FeeBurnOn(nd.Chain().Head())
		if live+feeBurn != genesisTotal {
			t.Fatalf("node %s conservation violated: live=%d feeBurn=%d total=%d",
				nd.Name, live, feeBurn, genesisTotal)
		}
	}
	post := c.nodes[0].StateAt().GetBalance(offender)
	if post != cur {
		t.Fatalf("expected sequential 1/3 slashes: post=%d want=%d", post, cur)
	}
	if got := c.nodes[0].StateAt().GetBalance(state.ProtocolAccount); got != pre-post {
		t.Fatalf("protocol public account = %d, want %d", got, pre-post)
	}
	t.Logf("slashed %x: %d -> %d; protocol account=%d; live+fees=%d conserved on all nodes",
		offender[:8], pre, post, pre-post, genesisTotal)

	// Forged/ill-formed evidence must never be accepted.
	set := consensus.NewSet(c.vals)
	if err := consensus.VerifyEvidenceExport(set, &types.Evidence{
		Kind: "vote", Offender: addr(3), Height: 9,
		A: []byte("junk"), B: []byte("jnuk"),
	}); err == nil {
		t.Fatal("forged evidence should fail verification")
	}
}
