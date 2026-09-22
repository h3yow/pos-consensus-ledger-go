package test_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
)

// TestFinalityThresholdBoundary：默认阈值严格大于 2/3。
// 权重 2/2/2（总 6）时 required = floor(6*2/3)+1 = 5：
// 两张票（权重 4）不得终局，三张票（权重 6）才终局。
func TestFinalityThresholdBoundary(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{2, 2, 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Engine.Threshold(), uint64(5); got != want {
		t.Fatalf("threshold = %d want %d", got, want)
	}

	if _, err := c.DriveNext(0); err != nil {
		t.Fatal(err)
	}
	// 只让两个节点处理该块与投票（权重 4 < 5），不应终局。
	deliverOnly(c, "n1", "n2")
	h1 := c.NodeByID["n1"].Head()
	w := c.NodeByID["n1"].Tally().WeightFor(
		slotOf(h1.Height, h1.Round), h1.Hash(), c.Engine.Validators())
	if w != 4 {
		t.Fatalf("two-voter weight = %d want 4", w)
	}
	if fh, fht := c.NodeByID["n1"].Finalized(); fht >= h1.Height {
		t.Fatalf("权重 4/6 不应终局，却终局到 h=%d %s", fht, fh)
	}
	t.Logf("判定：累计权重=%d < required=5 -> 不终局", w)

	// 让第三个节点也处理（权重 6 >= 5），立即终局。
	deliverOnly(c, "n3")
	c.Drain()
	fh, fht := c.NodeByID["n1"].Finalized()
	if fht < 1 {
		t.Fatalf("权重 6/6 应终局，实际 finalized h=%d %s", fht, fh)
	}
	t.Logf("判定：累计权重=6 >= required=5 -> 终局 h=%d block=%s", fht, fh)
}
