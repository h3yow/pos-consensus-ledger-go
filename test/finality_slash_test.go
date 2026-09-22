package test_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// TestFinalityProtectsFromRollback：超过 2/3 权益投票终局后，再注入
// 同一高度的竞争区块必须被丢弃，任何节点都不得回滚终局块。
func TestFinalityProtectsFromRollback(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{40, 30, 20, 10}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	for _, n := range c.Nodes {
		_, fht := n.Finalized()
		if fht < 1 {
			t.Fatalf("node %s 未终局到 h>=1 (got %d)", n.ID(), fht)
		}
	}
	ch, _ := c.Nodes[0].Store().Chain(c.Nodes[0].Head().Hash())
	final1 := ch[1]

	// 构造 h=1 r=1 的竞争区块（由调度的 r=1 proposer 签名，结构合法）。
	r1 := c.Engine.ScheduledProposer(1, 1)
	bad := c.CraftBlock(r1.Address, c.GenesisBlock, 1, 1, 0, nil)
	for _, n := range c.Nodes {
		n.HandleMessage(types.Message{Kind: types.MsgBlock, From: "byz", Block: bad})
	}
	for _, n := range c.Nodes {
		chain, err := n.Store().Chain(n.Head().Hash())
		if err != nil {
			t.Fatal(err)
		}
		if chain[1].Hash() != final1.Hash() {
			t.Fatalf("node %s 回滚了终局块: got %s want %s",
				n.ID(), chain[1].Hash(), final1.Hash())
		}
	}
	t.Logf("输入竞争块 h=1 r=1 hash=%s；判定丢弃，h=1 终局块 %s 不变（终局不可回滚）",
		bad.Hash(), final1.Hash())
}

// TestDoubleVoteSlashing：同一验证者在同槽对不同区块双签，证据被
// 检测、传播、打包并执行罚没；余额守恒、各节点状态根一致。
func TestDoubleVoteSlashing(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{40, 30, 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	total0 := c.Nodes[0].HeadState().TotalBalance()
	offender := c.Validator(0)
	balBefore := c.Nodes[0].HeadState().Balance(offender.Address)

	head := c.Nodes[0].Head()
	ch, _ := c.Nodes[0].Store().Chain(head.Hash())
	targetB := ch[0].Hash()
	byz := c.NodeByAddr[offender.Address]
	v1 := byz.ForceVote(3, 0, head.Hash())
	v2 := byz.ForceVote(3, 0, targetB)
	t.Logf("输入双签 h=3 r=0 blocks=%s vs %s offender=%s",
		v1.BlockHash, v2.BlockHash, offender.Address)
	c.Drain()

	var evHash types.Hash
	found := 0
	for _, n := range c.Nodes {
		for h, ev := range n.PendingEvidence() {
			if ev.Kind == types.EquivocationVote && ev.Offender() == offender.Address {
				evHash = h
				found++
			}
		}
	}
	if found == 0 {
		t.Fatal("没有任何节点检测到投票双签证据")
	}
	t.Logf("判定：%d 个节点独立得到同一证据 hash=%s", found, evHash)

	var slashedInBlock *types.Block
	for attempt := 0; attempt < 8; attempt++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
		h := c.Nodes[0].Head()
		if len(h.Evidence) > 0 {
			slashedInBlock = h
			break
		}
	}
	if slashedInBlock == nil {
		t.Fatal("证据始终未被打包")
	}
	saw := false
	for _, ev := range slashedInBlock.Evidence {
		if ev.Hash() == evHash {
			saw = true
		}
	}
	if !saw {
		t.Fatal("打包的证据 hash 与检测到的不一致")
	}

	wantSlash := balBefore * c.Genesis.SlashRateNumerator / c.Genesis.SlashRateDenominator
	root := c.Nodes[0].Head().StateRoot
	for _, n := range c.Nodes {
		st := n.HeadState()
		if got := st.Balance(offender.Address); got != balBefore-wantSlash {
			t.Fatalf("node %s offender 余额 %d want %d", n.ID(), got, balBefore-wantSlash)
		}
		if st.Balance(c.Genesis.Treasury) < wantSlash {
			t.Fatalf("node %s 国库未收到罚没", n.ID())
		}
		if st.TotalBalance() != total0 {
			t.Fatalf("node %s 总额不守恒 %d != %d", n.ID(), st.TotalBalance(), total0)
		}
		if n.Head().StateRoot != root {
			t.Fatalf("node %s 状态根不一致", n.ID())
		}
	}
	t.Logf("判定：区块 h=%d 罚没 %d（%d->%d），国库 +%d，总权益=%d 守恒，stateRoot=%s 全网一致",
		slashedInBlock.Height, wantSlash, balBefore, balBefore-wantSlash,
		wantSlash, total0, root)
}

// TestSameEvidenceSlashOnce：同一证据不能重复罚没。
func TestSameEvidenceSlashOnce(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{40, 30, 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	offender := c.Validator(0)
	head := c.Nodes[0].Head()
	ch, _ := c.Nodes[0].Store().Chain(head.Hash())
	byz := c.NodeByAddr[offender.Address]
	byz.ForceVote(3, 0, head.Hash())
	byz.ForceVote(3, 0, ch[0].Hash())
	c.Drain()

	for c.Nodes[0].Head().Height < 4 {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	if len(c.Nodes[0].Head().Evidence) == 0 {
		t.Fatal("h>=4 的区块应包含证据")
	}
	bal1 := c.Nodes[0].HeadState().Balance(offender.Address)

	for i := 0; i < 3; i++ {
		if _, err := c.DriveNext(0); err != nil {
			t.Fatal(err)
		}
		c.Drain()
	}
	bal2 := c.Nodes[0].HeadState().Balance(offender.Address)
	if bal2 != bal1 {
		t.Fatalf("证据被重复执行：余额 %d -> %d", bal1, bal2)
	}
	t.Logf("判定：重复证据不再罚没，offender 余额保持 %d", bal2)
}

// TestDoubleProposeEvidence：同 proposer 在同槽签两个不同区块，
// 必须检测为出块双签证据。
func TestDoubleProposeEvidence(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{40, 30, 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DriveNext(0); err != nil {
		t.Fatal(err)
	}
	c.Drain()

	parent := c.Nodes[0].Head()
	prop := c.Engine.ScheduledProposer(2, 0)
	a := c.CraftBlock(prop.Address, parent, 2, 0, 0, nil)
	b := c.CraftBlock(prop.Address, parent, 2, 0, 1, nil)
	if a.Hash() == b.Hash() {
		t.Fatal("两个不同时间戳的区块 hash 不应相同")
	}
	for _, blk := range []*types.Block{a, b} {
		for _, n := range c.Nodes {
			n.HandleMessage(types.Message{Kind: types.MsgBlock, From: "x", Block: blk})
		}
	}
	count := 0
	for _, n := range c.Nodes {
		for _, ev := range n.PendingEvidence() {
			if ev.Kind == types.EquivocationBlock && ev.Offender() == prop.Address {
				if !ev.Validator() {
					t.Fatal("出块双签证据校验失败")
				}
				count++
			}
		}
	}
	if count == 0 {
		t.Fatal("未检测到出块双签证据")
	}
	t.Logf("判定：同 proposer 同槽两区块 -> 出块双签证据被检测（%d 节点持有）", count)
}
