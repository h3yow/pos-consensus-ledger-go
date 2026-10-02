package node_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// deliverVote sends one vote message to every node and drains the network.
func (c *cluster) deliverVote(v *types.Vote, from string) {
	for _, nd := range c.nodes {
		c.net.SendTo(nd.Name, simnet.Message{Kind: simnet.MsgVote, From: from, Vote: v})
	}
	c.net.DeliverAll()
}

// TestForgedVoteRejected: nobody can cast a vote in a validator's name
// without that validator's private key. A garbage signature, a signature made
// with another validator's key, and an unknown voter are all rejected on every
// node; an honest re-broadcast of the SAME valid vote stays harmless.
func TestForgedVoteRejected(t *testing.T) {
	c := newCluster(t, 4)
	// Produce the h=1 block (self-applied by the proposer) and deliver it to
	// everyone, but do NOT cast honest votes: the attack is whether forged
	// votes for h=1 can be recorded in a validator's name.
	b1 := c.nodes[c.proposerIndex(1)].ProduceBlock()
	c.net.DeliverAll()
	c.assertSameHead()

	h := b1.Height
	target := types.HashBlock(b1)
	victim := c.nodes[0].Addr // A
	other := c.nodes[1].Addr  // B holds a DIFFERENT private key

	// 1) Garbage signature.
	forged1 := &types.Vote{Height: h, Voter: victim, BlockHash: target, Sig: make([]byte, types.SigLen)}
	c.deliverVote(forged1, "ATTACKER")

	// 2) Signed by B's key but claiming to be A.
	foreign := types.SignVote(&types.Vote{Height: h, Voter: victim, BlockHash: target}, c.keys[other])
	c.deliverVote(foreign, "ATTACKER")

	// 3) Unknown voter address.
	unknown := userAddr(7)
	privU := types.GenerateKey([]byte{0x75, 'u', 's', 'r', 7})
	stranger := types.SignVote(&types.Vote{Height: h, Voter: unknown, BlockHash: target}, privU)
	c.deliverVote(stranger, "ATTACKER")

	for _, nd := range c.nodes {
		if nd.Chain().Votes().HasVoted(h, victim) {
			t.Fatalf("node %s recorded a forged vote as validator A", nd.Name)
		}
		if nd.Chain().Votes().HasVoted(h, unknown) {
			t.Fatalf("node %s recorded a vote from a non-validator", nd.Name)
		}
	}
	t.Logf("decision: garbage sig, foreign-key-relabel, unknown voter -> rejected on all nodes; no evidence produced")

	// No node should have collected evidence for these (they were rejected,
	// not recorded), so no honest validator is queued for slashing.
	for _, nd := range c.nodes {
		for _, ev := range nd.PendingEvidence() {
			t.Fatalf("node %s queued evidence from rejected votes: %x", nd.Name, ev.Offender[:8])
		}
	}

	// 4) The honest validator's real vote is accepted afterwards, and its
	// identical re-broadcast is a harmless duplicate (not a double vote).
	honest := c.signVote(&types.Vote{Height: h, Voter: victim, BlockHash: target})
	c.deliverVote(honest, "A")
	for _, nd := range c.nodes {
		if !nd.Chain().Votes().HasVoted(h, victim) {
			t.Fatalf("node %s did not record A's genuine vote", nd.Name)
		}
	}
	c.deliverVote(honest, "RELAY")
	t.Logf("decision: genuine vote accepted; identical repeat broadcast treated as harmless duplicate")
}

// TestForgedProposalAndEvidenceRejected: a party without a validator's private
// key cannot make an accepted proposal in its name, and cannot assemble
// double-propose evidence that frames it. Blocks must carry a signature valid
// against the scheduled proposer's PUBLIC key; evidence with a wrong/foreign
// signature or mismatched fields is rejected identically everywhere.
func TestForgedProposalAndEvidenceRejected(t *testing.T) {
	c := newCluster(t, 4)
	b1 := c.step()

	p2 := c.proposerIndex(2)
	scheduled := c.nodes[p2]

	// Twin with the proposer's key builds a real competing proposal (the only
	// way to create VALID double-propose evidence).
	twin := c.twin(p2, "E")
	c.net.SendTo("E", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.DeliverAll()
	realA := scheduled.ProduceBlock()
	realB := twin.nd.ProduceBlockOn(types.HashBlock(b1), nil, nil)
	if realA == nil || realB == nil || types.HashBlock(realA) == types.HashBlock(realB) {
		t.Fatal("setup: need two distinct real proposals")
	}

	// 1) An UNSIGNED block claiming the scheduled proposer is rejected. Mutate
	// the body so it is not a hash-duplicate of the real proposal; then the
	// signature check (not duplicate handling) must reject it.
	unsigned := *realA
	unsigned.StateRoot[0] ^= 0xFF
	unsigned.Sig = nil
	for _, nd := range c.nodes {
		if err := nd.AddBlockDirect(&unsigned); !errors.Is(err, consensus.ErrBadProposerSig) {
			t.Fatalf("node %s: unsigned proposal err=%v, want ErrBadProposerSig", nd.Name, err)
		}
	}
	t.Logf("decision: unsigned proposal rejected with ErrBadProposerSig on all nodes")

	// 2) A block signed by a DIFFERENT key is rejected (cannot forge identity).
	foreignKey := c.keys[c.nodes[(p2+1)%len(c.nodes)].Addr]
	impostor := *realA
	impostor.StateRoot[0] ^= 0xFF // distinct body so duplicate check is bypassed
	types.SignBlock(&impostor, foreignKey)
	for _, nd := range c.nodes {
		if err := nd.AddBlockDirect(&impostor); !errors.Is(err, consensus.ErrBadProposerSig) {
			t.Fatalf("node %s: foreign-signed proposal err=%v", nd.Name, err)
		}
	}
	t.Logf("decision: proposal signed with another validator's key rejected (key/address binding)")

	// 3) Tampered double-propose evidence: take a valid pair, then alter one
	// payload. Every node must reject it and must NOT slash the victim.
	good, err := consensus.NewDoubleProposeEvidence(realA, realB)
	if err != nil {
		t.Fatal(err)
	}
	set := consensus.NewSet(c.vals)
	if err := consensus.VerifyEvidenceExport(set, good); err != nil {
		t.Fatalf("valid double-propose evidence rejected: %v", err)
	}
	tampered := *good
	tampered.B = append([]byte(nil), good.B...)
	if len(tampered.B) > 5 {
		tampered.B[len(tampered.B)-1] ^= 0xFF // flip a signature byte
	}
	if err := consensus.VerifyEvidenceExport(set, &tampered); err == nil {
		t.Fatal("tampered propose evidence accepted")
	}
	t.Logf("decision: tampered double-propose signature -> evidence rejected")

	// 4) Mismatched offender field: valid signatures from the proposer but an
	// evidence Offender pointing at another validator must be rejected.
	mismatch := *good
	mismatch.Offender = c.nodes[(p2+2)%len(c.nodes)].Addr
	if err := consensus.VerifyEvidenceExport(set, &mismatch); err == nil {
		t.Fatal("evidence with mismatched offender accepted — honest validator could be framed")
	}
	t.Logf("decision: offender field not bound to signing key -> rejected (no false slash)")

	// 5) A fabricated "propose" pair whose bodies are not signed blocks at all.
	junk := &types.Evidence{
		Kind: "propose", Offender: scheduled.Addr, Height: 2,
		A: []byte("not-a-signed-block"), B: []byte("also-not"),
	}
	if err := consensus.VerifyEvidenceExport(set, junk); err == nil {
		t.Fatal("fabricated junk propose evidence accepted")
	}
	t.Logf("decision: fabricated unsigned bodies rejected; honest validators cannot be framed")
}

// TestSlashToProtocolAccountConservationAndWeight drives a real double vote to
// finality and checks, on EVERY node:
//   - slashed stake lands in the protocol PUBLIC account (not burned), and that
//     account's balance is inside the committed state root;
//   - balances incl. public account + burned fees == genesis total;
//   - every node's post-slash state root and public-account balance agree;
//   - the offender's voting weight drops to its REMAINING balance.
func TestSlashToProtocolAccountConservationAndWeight(t *testing.T) {
	c := newCluster(t, 4)
	b1 := c.step()

	// Build an h=2 fork so there are two distinct vote targets.
	p2 := c.proposerIndex(2)
	twin := c.twin(p2, "E")
	c.net.SendTo("E", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.DeliverAll()
	b2a := c.nodes[p2].ProduceBlock()
	for _, nd := range c.nodes {
		for c.net.Pending(nd.Name) > 0 {
			c.net.Drop(nd.Name, 0)
		}
	}
	b2b := twin.nd.ProduceBlockOn(types.HashBlock(b1), nil, nil)
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.DeliverAll()
	c.net.SendTo("A", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("B", simnet.Message{Kind: simnet.MsgBlock, From: "E", Block: b2b})
	c.net.SendTo("C", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.SendTo("D", simnet.Message{Kind: simnet.MsgBlock, From: "P", Block: b2a})
	c.net.DeliverAll()
	winner := c.assertSameHead()
	loser := types.HashBlock(b2a)
	if winner == loser {
		loser = types.HashBlock(b2b)
	}
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()

	offender := c.nodes[3].Addr
	dTwin := newNode(c, "D2", offender)
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b1})
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b2a})
	c.net.SendTo("D2", simnet.Message{Kind: simnet.MsgBlock, From: "S", Block: b2b})
	c.net.DeliverAll()

	vWin := c.signVote(&types.Vote{Height: 2, Voter: offender, BlockHash: winner})
	vLose := c.signVote(&types.Vote{Height: 2, Voter: offender, BlockHash: loser})
	c.deliverVote(vWin, "D2")
	c.deliverVote(vLose, "D2")
	_ = dTwin

	genesisTotal := sumGenesis(c)
	before := c.gs.GetBalance(offender)

	included := false
	for h := uint64(3); h <= 14 && !included; h++ {
		blk := c.step()
		for _, ev := range blk.Evidence {
			if ev.Kind == "vote" && ev.Offender == offender {
				included = true
			}
		}
	}
	if !included {
		t.Fatal("double-vote evidence never included")
	}
	c.assertSameHead()

	// Determine how many times the offender was slashed along the canonical
	// chain (propose + vote may coincide) and the expected residual balance.
	wantBalance := before
	var anc []*consensus.BlockNode
	for n := c.nodes[0].Chain().Head(); n != nil && n.Block.Height > 0; n = n.Parent {
		anc = append(anc, n)
	}
	slashes := 0
	for i := len(anc) - 1; i >= 0; i-- {
		for _, ev := range anc[i].Block.Evidence {
			if ev.Offender == offender {
				wantBalance -= wantBalance / 3
				slashes++
			}
		}
	}
	wantCommon := before - wantBalance

	var root types.Hash
	for i, nd := range c.nodes {
		st := nd.StateAt()
		if st.GetBalance(offender) != wantBalance {
			t.Fatalf("node %s offender balance=%d want %d", nd.Name, st.GetBalance(offender), wantBalance)
		}
		if st.GetBalance(state.ProtocolAccount) != wantCommon {
			t.Fatalf("node %s public account=%d want %d", nd.Name,
				st.GetBalance(state.ProtocolAccount), wantCommon)
		}
		live := st.TotalBalance()
		feeBurn := nd.Chain().FeeBurnOn(nd.Chain().Head())
		if live+feeBurn != genesisTotal {
			t.Fatalf("node %s conservation: live=%d feeBurn=%d total=%d",
				nd.Name, live, feeBurn, genesisTotal)
		}
		r := nd.Head().StateRoot
		if i == 0 {
			root = r
		} else if r != root {
			t.Fatalf("node %s state root %x != %x (public-account balance disagreement)",
				nd.Name, r[:8], root[:8])
		}
	}
	t.Logf("decision: offender slashed %dx -> %d; protocol public account=%d included in root=%x",
		slashes, wantBalance, wantCommon, root[:8])
	t.Logf("decision: all nodes agree root and live+fees=%d == genesis total", genesisTotal)

	// Weight follows REMAINING available balance: the offender now contributes
	// exactly wantBalance to any justification quorum, not its original stake.
	head := c.nodes[0].Chain().Head()
	power := c.nodes[0].Chain().StateOf(head).GetBalance(offender)
	if power != wantBalance {
		t.Fatalf("voting weight %d != remaining balance %d", power, wantBalance)
	}
	if power >= before {
		t.Fatalf("slashed weight %d should be below original stake %d", power, before)
	}
	t.Logf("decision: offender voting weight reduced to remaining balance %d (was %d)", power, before)
}
