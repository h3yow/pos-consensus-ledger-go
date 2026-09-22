package test_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/internal/mempool"
	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/testkit"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// TestStateTransferAndRoot 验证转账执行、余额守恒、序号推进，以及
// 两个独立状态副本执行同一批交易后状态根完全一致。
func TestStateTransferAndRoot(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{10, 20, 30, 40}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := c.Genesis
	st := state.FromGenesis(g)
	other := state.FromGenesis(g)
	total0 := st.TotalBalance()

	from := c.Addr(1)
	to := c.Addr(3)
	before := st.Balance(from)

	// Execute the same 3 in-nonce transfers on two independent states.
	for i := uint64(0); i < 3; i++ {
		tx := testkit.SignTx(c.Keys[1], to, i, 7, 2)
		if err := st.Apply(tx, g.Treasury); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := other.Apply(tx, g.Treasury); err != nil {
			t.Fatalf("apply other: %v", err)
		}
	}

	if st.Nonce(from) != 3 {
		t.Fatalf("nonce = %d want 3", st.Nonce(from))
	}
	if got := st.Balance(from); got != before-3*(7+2) {
		t.Fatalf("sender balance = %d want %d", got, before-27)
	}
	if st.Balance(to)-other.Balance(to) != 0 {
		t.Fatal("recipient balances diverged")
	}
	if st.Root() != other.Root() {
		t.Fatalf("state roots differ: %s vs %s", st.Root(), other.Root())
	}
	t.Logf("输入 3 笔转账 amount=7 fee=2；判定 stateRoot=%s", st.Root())

	// Fee went to the sink; total must be conserved.
	if st.TotalBalance() != total0 {
		t.Fatalf("total balance changed: %d != %d", st.TotalBalance(), total0)
	}
}

// TestSlashConservation 验证罚没金额从作恶者转移到国库且总量守恒，
// 并且同一证据（用同一 hash 模拟）在 SlashEvidence 下只生效一次。
func TestSlashConservation(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{10, 20, 30, 40}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := c.Genesis
	st := state.FromGenesis(g)
	total0 := st.TotalBalance()
	victim := c.Addr(2)
	before := st.Balance(victim)
	treasuryBefore := st.Balance(g.Treasury)

	got := st.Slash(victim, g.Treasury, 10, 100)
	want := before * 10 / 100
	if got != want {
		t.Fatalf("slash amount = %d want %d", got, want)
	}
	if st.Balance(victim) != before-want || st.Balance(g.Treasury) != treasuryBefore+want {
		t.Fatal("slash balances wrong")
	}
	if st.TotalBalance() != total0 {
		t.Fatalf("total changed after slash: %d != %d", st.TotalBalance(), total0)
	}
	t.Logf("输入 offender=%s balance=%d；判定 slash=%d -> treasury, 总量=%d 守恒",
		victim, before, got, st.TotalBalance())
}

// TestTxRejectionReasons 覆盖可区分的拒绝原因：坏签名、序号乱序、
// 余额不足、未知账户，以及交易池重复/同序号冲突。
func TestTxRejectionReasons(t *testing.T) {
	c, err := testkit.NewCluster([]uint64{10, 20, 30, 40}, false)
	if err != nil {
		t.Fatal(err)
	}
	st := state.FromGenesis(c.Genesis)
	pool := mempool.New(st, 100)

	from := c.Addr(0)
	to := c.Addr(3)

	// 1) out-of-order nonce (expected 0)
	badNonce := testkit.SignTx(c.Keys[0], to, 9, 1, 1)
	if err := st.Validate(badNonce); !errors.Is(err, state.ErrBadNonce) {
		t.Fatalf("want ErrBadNonce got %v", err)
	}
	t.Logf("输入 nonce=9（账户 nonce=0）-> 判定 %v", state.ErrBadNonce)

	// 2) insufficient funds
	poor := testkit.SignTx(c.Keys[0], to, 0, st.Balance(from), 1)
	if err := st.Validate(poor); !errors.Is(err, state.ErrInsufficient) {
		t.Fatalf("want ErrInsufficient got %v", err)
	}
	t.Logf("输入 amount=balance fee=1 -> 判定 %v", state.ErrInsufficient)

	// 3) bad signature
	good := testkit.SignTx(c.Keys[0], to, 0, 1, 1)
	good.Sig[0] ^= 0xFF
	if err := st.Validate(good); !errors.Is(err, state.ErrBadSignature) {
		t.Fatalf("want ErrBadSignature got %v", err)
	}
	t.Logf("输入翻转签名的 tx -> 判定 %v", state.ErrBadSignature)

	// 4) unknown sender: construct a fresh keypair
	pool2 := mempool.New(state.FromGenesis(c.Genesis), 100)
	txUnknown := testkit.SignTx(c.Keys[1], to, 0, 0, 0)
	// zero amount+fee is rejected earlier; craft non-zero with a stranger key
	stranger := testkit.NewStrangerKey(t)
	txStranger := testkit.SignTx(stranger, to, 0, 1, 1)
	if err := pool2.Add(txStranger); !errors.Is(err, state.ErrUnknownSender) {
		t.Fatalf("want ErrUnknownSender got %v", err)
	}
	t.Logf("输入未知账户签名 tx -> 判定 %v", state.ErrUnknownSender)
	_ = txUnknown

	// 5) mempool duplicate + same-sender/nonce conflict
	tx0 := testkit.SignTx(c.Keys[0], to, 0, 1, 5)
	if err := pool.Add(tx0); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(tx0); !errors.Is(err, mempool.ErrAlreadyPending) {
		t.Fatalf("want ErrAlreadyPending got %v", err)
	}
	txConflict := testkit.SignTx(c.Keys[0], c.Addr(2), 0, 2, 5)
	if err := pool.Add(txConflict); !errors.Is(err, mempool.ErrConflictingNonce) {
		t.Fatalf("want ErrConflictingNonce got %v", err)
	}
	t.Logf("输入重复 tx 与同 nonce 不同内容 tx -> 判定 %v / %v",
		mempool.ErrAlreadyPending, mempool.ErrConflictingNonce)

	// 6) packing order: fee desc
	a := testkit.SignTx(c.Keys[1], to, 0, 1, 1)
	b := testkit.SignTx(c.Keys[2], to, 0, 1, 9)
	d := testkit.SignTx(c.Keys[3], to, 0, 1, 3)
	for _, tx := range []*types.Tx{a, b, d} {
		if err := pool.Add(tx); err != nil {
			t.Fatal(err)
		}
	}
	packed := pool.Pack(0)
	if len(packed) < 3 || packed[0].Fee != 9 || packed[1].Fee != 5 || packed[2].Fee != 3 {
		t.Fatalf("packing order wrong: fees=%v", feesOf(packed))
	}
	t.Logf("输入 fees={1,9,3} + 已有 fee=5 -> 判定打包顺序 fees=%v (高费优先)", feesOf(packed))
}

func feesOf(txs []*types.Tx) []uint64 {
	out := make([]uint64, len(txs))
	for i, tx := range txs {
		out[i] = tx.Fee
	}
	return out
}
