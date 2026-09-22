package test_test

import (
	"sync"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/internal/node"
	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// TestConcurrentMessageDelivery 在多个 goroutine 中并发投递重复的
// 区块/投票/交易消息，配合 -race 验证节点内部状态无数据竞争，且最终
// 各节点规范链与状态根一致。不依赖时序：投递完成后再统一断言。
func TestConcurrentMessageDelivery(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{40, 30, 20, 10}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Build one block, let it propagate, then capture it.
	if _, err := c.DriveNext(0); err != nil {
		t.Fatal(err)
	}
	c.Drain()
	blks := c.Nodes[0].Store().BlocksAt(1)
	if len(blks) != 1 {
		t.Fatalf("setup: %d blocks at h=1", len(blks))
	}
	blk := blks[0]

	var wg sync.WaitGroup
	for rep := 0; rep < 8; rep++ {
		for _, n := range c.Nodes {
			wg.Add(1)
			go func(target *node.Node, r int) {
				defer wg.Done()
				target.HandleMessage(types.Message{Kind: types.MsgBlock, From: "race", Block: blk})
			}(n, rep)
		}
	}
	// Concurrent tx submissions (same tx repeated -> idempotent reject).
	tx := testkit.SignTx(c.Keys[1], c.Addr(2), 0, 1, 1)
	for rep := 0; rep < 8; rep++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Nodes[rep%len(c.Nodes)].SubmitTx(tx)
		}()
	}
	wg.Wait()
	c.Drain()

	for _, n := range c.Nodes {
		if len(n.Store().BlocksAt(1)) != 1 {
			t.Fatalf("node %s 存储了 %d 个 h=1 区块", n.ID(), len(n.Store().BlocksAt(1)))
		}
	}
	if diff := headsEqual(c); diff != "" {
		t.Fatalf("并发投递后规范链在 %s 不一致", diff)
	}
	t.Logf("判定：8 路并发重复投递后，每节点仅 1 个 h=1 区块，head=%s 一致",
		c.Nodes[0].Head().Hash())
}

// TestConcurrentProposalsSameHeight 模拟多个验证者“同时”为同一高度
// 出块（不同 round），消息以交错顺序到达，所有节点仍必须按确定性规则
// 选出同一个规范区块并终局。
func TestConcurrentProposalsSameHeight(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{34, 33, 33}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Common prefix height 1.
	if _, err := c.DriveNext(0); err != nil {
		t.Fatal(err)
	}
	c.Drain()

	parent := c.Nodes[0].Head() // h=1
	p0 := c.Engine.ScheduledProposer(2, 0)
	p1 := c.Engine.ScheduledProposer(2, 1)
	p2 := c.Engine.ScheduledProposer(2, 2)
	b0 := c.CraftBlock(p0.Address, parent, 2, 0, 0, nil)
	b1 := c.CraftBlock(p1.Address, parent, 2, 1, 0, nil)
	b2 := c.CraftBlock(p2.Address, parent, 2, 2, 0, nil)
	t.Logf("输入同高度竞争区块：r0=%s r1=%s r2=%s", b0.Hash(), b1.Hash(), b2.Hash())

	// Deliver in different interleaved orders per node: give every node
	// all three blocks but in a node-specific permutation.
	perms := [][]*types.Block{
		{b0, b1, b2},
		{b2, b0, b1},
		{b1, b2, b0},
	}
	var wg sync.WaitGroup
	for i, n := range c.Nodes {
		wg.Add(1)
		go func(idx int, target *node.Node) {
			defer wg.Done()
			for _, blk := range perms[idx%len(perms)] {
				target.HandleMessage(types.Message{Kind: types.MsgBlock, From: "x", Block: blk})
			}
		}(i, n)
	}
	wg.Wait()
	c.Drain()

	for _, n := range c.Nodes {
		if n.Head().Hash() != b0.Hash() {
			t.Fatalf("node %s head=%s 未选择最低 round 区块 %s", n.ID(), n.Head().Hash(), b0.Hash())
		}
	}
	t.Logf("判定：三种不同投递顺序下，全部节点都选择 r=0 区块 %s", b0.Hash())
}
