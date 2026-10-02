package consensus_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func idAddr(i byte) types.Address { var a types.Address; a[19] = i; return a }

// TestVoteSignatureBinding proves that only the holder of a validator's
// private key can cast a vote others accept, and that a tampered or
// re-attributed signature fails.
func TestVoteSignatureBinding(t *testing.T) {
	signer := idAddr(1)
	honest := idAddr(2)
	set := consensus.NewSet([]*types.Validator{v(1, 1000), v(2, 1000)})

	// Honest, key-signed vote verifies.
	good := identity.SignVote(&types.Vote{Height: 7, Voter: signer, BlockHash: types.Hash{1: 1}},
		identity.PrivateKeyFor(signer))
	if !set.VerifyVote(good) {
		t.Fatal("genuine vote must verify")
	}

	// Attacker re-labels the vote as the honest validator but keeps the old
	// signature: verification against honest's key fails.
	forged := *good
	forged.Voter = honest
	if set.VerifyVote(&forged) {
		t.Fatal("vote with mismatched signer must not verify")
	}

	// Attacker tampers the target block hash after signing.
	tampered := *good
	tampered.BlockHash[0] = 0xFF
	if set.VerifyVote(&tampered) {
		t.Fatal("tampered vote must not verify")
	}

	// Attacker flips a signature byte.
	badsig := *good
	badsig.Sig[0] ^= 0xFF
	if set.VerifyVote(&badsig) {
		t.Fatal("bad signature must not verify")
	}

	// An unknown voter (no published key) is rejected.
	unknown := identity.SignVote(&types.Vote{Height: 7, Voter: idAddr(99), BlockHash: types.Hash{}},
		identity.PrivateKeyFor(idAddr(99)))
	if set.VerifyVote(unknown) {
		t.Fatal("vote from unknown identity must not verify")
	}
	t.Logf("decision: genuine vote accepted; re-attributed/tampered/bad-sig/unknown votes all rejected")
}

// TestProposerSignatureBinding covers block proposals: only the scheduled
// proposer's key produces an acceptable signature; copying another
// proposer's signature or mutating the body fails.
func TestProposerSignatureBinding(t *testing.T) {
	prop := idAddr(1)
	set := consensus.NewSet([]*types.Validator{v(1, 1000), v(2, 1000)})

	mkBlock := func(proposer types.Address, rootByte byte) *types.Block {
		b := &types.Block{Height: 1, Proposer: proposer}
		b.StateRoot[9] = rootByte
		identity.SignBlock(b, identity.PrivateKeyFor(proposer))
		return b
	}
	b := mkBlock(prop, 9)
	if !set.VerifyBlock(b) {
		t.Fatal("proposer-signed block must verify")
	}

	// Mutate the body after signing.
	tampered := *b
	tampered.StateRoot[9] = 8
	if set.VerifyBlock(&tampered) {
		t.Fatal("body mutated after signing must fail")
	}

	// Re-attribute the block to another validator without its key.
	impersonated := *b
	impersonated.Proposer = idAddr(2)
	if set.VerifyBlock(&impersonated) {
		t.Fatal("block with another proposer's signature must fail")
	}

	// Correct key but unsigned.
	unsigned := &types.Block{Height: 1, Proposer: prop, StateRoot: types.Hash{1: 1}}
	if set.VerifyBlock(unsigned) {
		t.Fatal("unsigned block must fail")
	}
	t.Logf("decision: proposer signature binds the whole block; tamper/re-attribute/unsigned rejected")
}

// TestEvidenceCannotFrameHonestValidator assembles evidence an attacker might
// use to frame a validator, and shows every node rejects it:
//   - garbage payloads;
//   - offender field swapped to an honest validator (fields don't match);
//   - both votes signed by the attacker, claimed to be the honest one;
//   - double-propose evidence whose blocks carry the attacker's signature.
func TestEvidenceCannotFrameHonestValidator(t *testing.T) {
	attacker := idAddr(1)
	honest := idAddr(2)
	set := consensus.NewSet([]*types.Validator{v(1, 1000), v(2, 1000)})
	attPriv := identity.PrivateKeyFor(attacker)

	// Garbage.
	if err := set.VerifyEvidenceExport(&types.Evidence{
		Kind: "vote", Offender: honest, Height: 5,
		A: []byte("junk"), B: []byte("jnuk"),
	}); err == nil {
		t.Fatal("garbage evidence must be rejected")
	}

	// Attacker signs two conflicting votes but claims honest did it.
	va := identity.SignVote(&types.Vote{Height: 5, Voter: attacker, BlockHash: types.Hash{1: 1}}, attPriv)
	vb := identity.SignVote(&types.Vote{Height: 5, Voter: attacker, BlockHash: types.Hash{1: 2}}, attPriv)
	ev := consensus.NewVoteEvidenceExport(va, vb)
	ev.Offender = honest // frame attempt: signatures do not match honest's key
	if err := set.VerifyEvidenceExport(ev); err == nil {
		t.Fatal("evidence signed by attacker but attributed to honest must be rejected")
	}
	// The same evidence against the real signer (attacker) is valid, proving
	// the signatures themselves were never broken — only the framing fails.
	ev.Offender = attacker
	if err := set.VerifyEvidenceExport(ev); err != nil {
		t.Fatalf("attacker's genuine equivocation should be slashable: %v", err)
	}

	// Double-propose frame: two blocks signed by attacker, claimed as honest.
	mkBlock := func(salt byte) *types.Block {
		b := &types.Block{Height: 5, Proposer: attacker}
		b.StateRoot[0] = salt
		identity.SignBlock(b, attPriv)
		return b
	}
	pe, err := consensus.NewDoubleProposeEvidence(mkBlock(1), mkBlock(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := set.VerifyEvidenceExport(pe); err != nil {
		t.Fatalf("attacker's own double-propose must verify: %v", err)
	}
	pe.Offender = honest // re-attribute: signatures no longer match
	if err := set.VerifyEvidenceExport(pe); err == nil {
		t.Fatal("double-propose signed by attacker but attributed to honest must be rejected")
	}
	t.Logf("decision: garbage/framed evidence rejected on all nodes; honest validator never slashable without its key")
}

// TestDuplicateVoteHarmless checks that the exact same vote repeated is
// accepted as an idempotent duplicate rather than treated as equivocation.
func TestDuplicateVoteHarmless(t *testing.T) {
	// Use the chain-bound vote pool via a minimal genesis chain.
	set := consensus.NewSet([]*types.Validator{v(1, 1000)})
	vp := consensus.NewVotePoolExport(set)
	voter := idAddr(1)
	vote := identity.SignVote(&types.Vote{Height: 3, Voter: voter, BlockHash: types.Hash{3: 3}},
		identity.PrivateKeyFor(voter))

	if ev, err := vp.AddVote(vote); ev != nil || err != nil {
		t.Fatalf("first vote: ev=%v err=%v", ev, err)
	}
	if ev, err := vp.AddVote(vote); ev != nil || err != nil {
		t.Fatalf("identical repeat must be harmless, got ev=%v err=%v", ev, err)
	}
	t.Logf("decision: identical repeat vote = harmless duplicate (no evidence, no error)")
}
