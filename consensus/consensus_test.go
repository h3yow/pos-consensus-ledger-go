package consensus_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func v(i byte, stake uint64) *types.Validator {
	var a types.Address
	a[19] = i
	return &types.Validator{Addr: a, Stake: stake, PubKey: identity.PublicKeyFor(a)}
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
	v1, v2 := byte(1), byte(2)
	if counts[func() types.Address { var a types.Address; a[19] = v1; return a }()] != 100 ||
		counts[func() types.Address { var a types.Address; a[19] = v2; return a }()] != 300 {
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
	var offender types.Address
	offender[19] = 7
	var h1, h2 types.Hash
	h1[0] = 1
	h2[0] = 2
	priv := identity.PrivateKeyFor(offender)
	va := identity.SignVote(&types.Vote{Height: 5, Voter: offender, BlockHash: h1}, priv)
	vb := identity.SignVote(&types.Vote{Height: 5, Voter: offender, BlockHash: h2}, priv)
	set := consensus.NewSet([]*types.Validator{v(7, 10)})

	ev := consensus.NewVoteEvidenceExport(va, vb)
	if err := set.VerifyEvidenceExport(ev); err != nil {
		t.Fatalf("valid double-vote evidence rejected: %v", err)
	}

	// Same target hash is not equivocation.
	same := consensus.NewVoteEvidenceExport(va, va)
	if err := set.VerifyEvidenceExport(same); err == nil {
		t.Fatal("identical votes must not be slashable evidence")
	}

	// Tampered signature fails.
	vbBad := *vb
	vbBad.Sig[0] ^= 0xFF
	bad := consensus.NewVoteEvidenceExport(va, &vbBad)
	if err := set.VerifyEvidenceExport(bad); err == nil {
		t.Fatal("forged evidence with bad signature accepted")
	}

	// Evidence with the offender field swapped onto an honest validator must
	// fail: the votes are signed by offender[7], not by the claimed offender.
	var honest types.Address
	honest[19] = 8
	set2 := consensus.NewSet([]*types.Validator{v(7, 10), v(8, 10)})
	framed := consensus.NewVoteEvidenceExport(va, vb)
	framed.Offender = honest
	if err := set2.VerifyEvidenceExport(framed); err == nil {
		t.Fatal("evidence with mismatched offender must be rejected")
	}
}
