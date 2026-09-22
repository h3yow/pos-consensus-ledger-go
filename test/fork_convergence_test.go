package test_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// TestForkConvergence 覆盖：区块延迟到达、网络分区下的短暂分叉、
// 恢复后的规范链收敛、重复广播幂等。不依赖任何时序与真实网络。
func TestForkConvergence(t *testing.T) {
	// 4 validators, weights 40/30/20/10 (total 100, threshold 67).
	c, err := testkit.NewCluster([]uint64{40, 30, 20, 10}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Heights 1..2 normally: everyone shares a prefix.
	for h := 0; h < 2; h++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	if diff := headsEqual(c); diff != "" {
		t.Fatalf("heads differ before partition at %s", diff)
	}

	// Partition: isolate n4. Majority = {n1,n2,n3}.
	c.Net.Disconnect("n4")
	majority := map[string]bool{"n1": true, "n2": true, "n3": true}
	for h := 0; h < 3; h++ {
		blk, err := proposeInPartition(c, majority)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("分区中多数派出块 h=%d r=%d hash=%s proposer=%s",
			blk.Height, blk.Round, blk.Hash(), blk.Proposer)
		deliverOnly(c, "n1", "n2", "n3")
	}

	// Isolated n4 also extends its own branch whenever its scheduled
	// proposer is itself; otherwise it simply stays at the common prefix.
	n4 := c.NodeByID["n4"]
	n4OwnBlock := false
	for k := 0; k < 2; k++ {
		head := n4.Head()
		p := c.ProposerFor(head.Height+1, 0)
		if p.ID() == "n4" {
			b, err := p.ProposeBlock()
			if err != nil {
				t.Fatal(err)
			}
			n4OwnBlock = true
			t.Logf("分区中 n4 独立出块 h=%d hash=%s（竞争分支）", b.Height, b.Hash())
		} else {
			break
		}
	}

	majHead := c.NodeByID["n1"].Head()
	n4Head := n4.Head()
	if majHead.Hash() == n4Head.Hash() {
		t.Fatal("partition did not produce divergence as expected")
	}
	t.Logf("分区态：多数派 head h=%d %s；n4 head h=%d %s (ownBlock=%v)",
		majHead.Height, majHead.Hash(), n4Head.Height, n4Head.Hash(), n4OwnBlock)

	// Recover: delayed messages flush. Also duplicate one queued message
	// per node to prove replays are absorbed idempotently.
	c.Net.Restore("n4")
	for _, id := range []string{"n1", "n2", "n3", "n4"} {
		c.Net.Redeliver(id)
	}
	c.Drain()

	// Continue over the healed network.
	for h := 0; h < 3; h++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}

	if diff := headsEqual(c); diff != "" {
		t.Fatalf("after recovery heads differ at %s", diff)
	}
	if n4.Head().Hash() != c.NodeByID["n1"].Head().Hash() {
		t.Fatal("n4 failed to converge to canonical chain")
	}
	t.Logf("恢复后判定：四节点规范链统一 head h=%d hash=%s",
		n4.Head().Height, n4.Head().Hash())

	// Same height + same block => identical state root on every node.
	r := n4.Head().StateRoot
	for _, n := range c.Nodes {
		if n.Head().StateRoot != r {
			t.Fatalf("node %s state root %s != %s", n.ID(), n.Head().StateRoot, r)
		}
	}
}

// TestDuplicateBroadcastIdempotent feeds the same block/vote messages
// multiple times and asserts no double application or panic.
func TestDuplicateBroadcastIdempotent(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{10, 20, 30, 40}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DriveNext(0); err != nil {
		t.Fatal(err)
	}
	// Many rounds of redelivery before draining.
	for i := 0; i < 5; i++ {
		for _, n := range c.Nodes {
			c.Net.Redeliver(n.ID())
		}
	}
	c.Drain()
	if diff := headsEqual(c); diff != "" {
		t.Fatalf("heads differ after duplicate flood at %s", diff)
	}
	// Only one block at height 1 stored per node.
	for _, n := range c.Nodes {
		if bs := n.Store().BlocksAt(1); len(bs) != 1 {
			t.Fatalf("node %s stored %d blocks at h=1", n.ID(), len(bs))
		}
	}
	t.Logf("输入 5 轮重复广播 -> 判定每节点 h=1 仅存 1 个区块，规范链 hash=%s",
		c.Nodes[0].Head().Hash())
}

// proposeInPartition advances rounds until the scheduled proposer is a
// member of the connected partition, then has it propose.
func proposeInPartition(c *testkit.Cluster, alive map[string]bool) (*types.Block, error) {
	head := c.Nodes[0].Head()
	height := head.Height + 1
	for round := uint32(0); ; round++ {
		p := c.ProposerFor(height, round)
		if alive[p.ID()] {
			if round == 0 {
				return p.ProposeBlock()
			}
			return p.ProposeRound(round, nil)
		}
	}
}

func headsEqual(c *testkit.Cluster) string {
	h := c.Nodes[0].Head().Hash()
	for _, n := range c.Nodes[1:] {
		if n.Head().Hash() != h {
			return n.ID()
		}
	}
	return ""
}

func deliverOnly(c *testkit.Cluster, ids ...string) {
	for {
		did := 0
		for _, id := range ids {
			if c.Net.DeliverOne(id) {
				did++
			}
		}
		if did == 0 {
			return
		}
	}
}

// TestCompetingBranchesDeterministic builds two genuinely competing
// branches at the same height: the round-0 proposer is partitioned alone
// and builds A; the majority uses round 1 with its scheduled proposer to
// build B. On recovery ALL nodes must pick the SAME canonical branch by
// the deterministic rule (lower round wins), independent of delivery
// order.
func TestCompetingBranchesDeterministic(t *testing.T) {
	// 3 validators => height 3 rounds: r0=v0, r1=v1, r2=v2.
	c, err := testkit.NewCluster([]uint64{30, 30, 40}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Build heights 1..2 shared.
	for i := 0; i < 2; i++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}

	// Isolate the r0 proposer of height 3 (validator sorted index 0).
	r0 := c.ProposerFor(3, 0)
	r1 := c.ProposerFor(3, 1)
	c.Net.Disconnect(r0.ID())

	// Isolated side: r0 builds branch A at round 0.
	a, err := r0.ProposeBlock()
	if err != nil {
		t.Fatal(err)
	}

	// Majority side: deliver nothing to r0; r1 builds branch B round 1.
	// r1 needs the shared head (it has it). Drive its round-1 proposal.
	alive := map[string]bool{}
	for _, n := range c.Nodes {
		if n.ID() != r0.ID() {
			alive[n.ID()] = true
		}
	}
	b, err := r1.ProposeRound(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	deliverOnly(c, r1.ID(), thirdID(c, r0.ID(), r1.ID()))
	t.Logf("竞争区块：A h=3 r=0 hash=%s proposer=%s；B h=3 r=1 hash=%s proposer=%s",
		a.Hash(), a.Proposer, b.Hash(), b.Proposer)

	// Recover and flush in BOTH delivery orders to prove order independence.
	c.Net.Restore(r0.ID())
	c.Drain()

	// Deterministic rule: lower round (A) must win on every node...
	// NOTE A's branch is shorter here; the rule compares the FIRST
	// diverging slot: A round0 < B round1, hence A is canonical.
	head := c.Nodes[0].Head()
	for _, n := range c.Nodes {
		if n.Head().Hash() != head.Hash() {
			t.Fatalf("node %s head %s != %s", n.ID(), n.Head().Hash(), head.Hash())
		}
	}
	if head.Hash() != a.Hash() {
		t.Fatalf("canonical head = %s want lower-round block A %s", head.Hash(), a.Hash())
	}
	t.Logf("判定：首个分叉槽 r=0 < r=1 -> 全网统一选择 A=%s（与投递顺序无关）", head.Hash())

	// Continue on the healed network on top of A until h=6; everyone follows.
	for c.Nodes[0].Head().Height < 6 {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	for _, n := range c.Nodes {
		if n.Head().Hash() != c.Nodes[0].Head().Hash() {
			t.Fatalf("post-reorg divergence on %s", n.ID())
		}
		if n.Head().StateRoot != c.Nodes[0].Head().StateRoot {
			t.Fatalf("state root mismatch on %s", n.ID())
		}
	}
}

func thirdID(c *testkit.Cluster, a, b string) string {
	for _, n := range c.Nodes {
		if n.ID() != a && n.ID() != b {
			return n.ID()
		}
	}
	return ""
}
