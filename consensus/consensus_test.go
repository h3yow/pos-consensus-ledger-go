package consensus_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func vkey(i byte) (types.Address, ed25519.PrivateKey) {
	priv := types.GenerateKey([]byte{0x63, 't', i})
	pub := priv.Public().(ed25519.PublicKey)
	return types.AddressOf(pub), priv
}

func v(i byte, stake uint64) *types.Validator {
	a, priv := vkey(i)
	return &types.Validator{Addr: a, Stake: stake, PubKey: priv.Public().(ed25519.PublicKey)}
}

func signWith(i byte, vote *types.Vote) *types.Vote {
	_, priv := vkey(i)
	return types.SignVote(vote, priv)
}

// TestWeightedRoundRobin: a 3x staker proposes ~3x as often and the schedule
// is identical on repeated computation (deterministic).
func TestWeightedRoundRobin(t *testing.T) {
	set := consensus.NewSet([]*types.Validator{v(1, 1), v(2, 3)})
	if set.TotalStake() != 4 {
		t.Fatalf("total = %d, want 4", set.TotalStake())
	}
	counts := map[types.Address]int{}
	var seq []types.Address
	for h := uint64(1); h <= 400; h++ {
		p := set.ProposerAt(h)
		counts[p]++
		seq = append(seq, p)
	}
	// 3:1 proposal ratio and strict periodicity every totalStake heights.
	a1, _ := vkey(1)
	a2, _ := vkey(2)
	if counts[a1] != 100 || counts[a2] != 300 {
		t.Fatalf("counts = %v, want {v1:100 v2:300}", counts)
	}
	for h := uint64(1); h+4 <= 400; h++ {
		if set.ProposerAt(h) != set.ProposerAt(h+4) {
			t.Fatalf("schedule not periodic at %d", h)
		}
	}
	// A second independent set yields the identical sequence (cross-node).
	set2 := consensus.NewSet([]*types.Validator{v(1, 1), v(2, 3)})
	for h := uint64(1); h <= 400; h++ {
		if set.ProposerAt(h) != set2.ProposerAt(h) {
			t.Fatal("independent sets disagree on proposer")
		}
	}
}

func TestQuorum(t *testing.T) {
	cases := map[uint64]uint64{
		3:  3, // floor(6/3)+1
		4:  3, // floor(8/3)+1
		6:  5, // floor(12/3)+1
		9:  7,
		12: 9,
	}
	for total, want := range cases {
		if got := consensus.QuorumExport(total); got != want {
			t.Errorf("quorum(%d)=%d want %d", total, got, want)
		}
	}
}

func TestEvidenceVerification(t *testing.T) {
	offender, _ := vkey(7)
	var h1, h2 types.Hash
	h1[0] = 1
	h2[0] = 2
	va := signWith(7, &types.Vote{Height: 5, Voter: offender, BlockHash: h1})
	vb := signWith(7, &types.Vote{Height: 5, Voter: offender, BlockHash: h2})

	set := consensus.NewSet([]*types.Validator{v(7, 1)})
	ev := consensus.NewVoteEvidenceExport(va, vb)
	if err := consensus.VerifyEvidenceExport(set, ev); err != nil {
		t.Fatalf("valid double-vote evidence rejected: %v", err)
	}

	// Same target hash is not equivocation.
	same := consensus.NewVoteEvidenceExport(va, va)
	if err := consensus.VerifyEvidenceExport(set, same); err == nil {
		t.Fatal("identical votes must not be slashable evidence")
	}

	// Tampered signature fails.
	vbBad := *vb
	vbBad.Sig[0] ^= 0xFF
	bad := consensus.NewVoteEvidenceExport(va, &vbBad)
	if err := consensus.VerifyEvidenceExport(set, bad); err == nil {
		t.Fatal("forged evidence with bad signature accepted")
	}

	// A vote signed by ANOTHER validator's key but relabelled as offender 7
	// must fail: nobody can frame an honest validator without its private key.
	other, _ := vkey(8)
	foreign := signWith(8, &types.Vote{Height: 5, Voter: offender, BlockHash: h2})
	if types.VerifyVote(foreign, set.PubKey(offender)) {
		t.Fatal("relabelled foreign signature must not verify as offender's")
	}
	frame := consensus.NewVoteEvidenceExport(va, foreign)
	if err := consensus.VerifyEvidenceExport(set, frame); err == nil {
		t.Fatal("evidence mixing another key should be rejected")
	}
	_ = other
}
