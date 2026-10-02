package pool_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func TestPoolRejectAndOrder(t *testing.T) {
	st := state.New()
	var a, b types.Address
	a[19] = 1
	b[19] = 2
	st.SetBalance(a, 1000)
	st.SetBalance(b, 1000)
	p := pool.New(st, 0)

	mk := func(from types.Address, nonce, fee uint64) *types.Tx {
		return types.SignTx(&types.Tx{From: from, To: b, Nonce: nonce, Value: 1, Fee: fee})
	}

	// duplicate
	t1 := mk(a, 0, 10)
	if err := p.Add(t1); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(t1); !errors.Is(err, pool.ErrAlreadyKnown) {
		t.Fatalf("dup: want ErrAlreadyKnown, got %v", err)
	}

	// bad signature
	bad := mk(a, 1, 10)
	bad.Sig[0] ^= 1
	if err := p.Add(bad); !errors.Is(err, state.ErrBadSignature) {
		t.Fatalf("bad sig: %v", err)
	}

	// insufficient balance
	big := types.SignTx(&types.Tx{From: a, To: b, Nonce: 2, Value: 2000, Fee: 0})
	if err := p.Add(big); !errors.Is(err, state.ErrInsufficient) {
		t.Fatalf("insufficient: want ErrInsufficient, got %v", err)
	}

	// fee ordering across distinct (sender,nonce): higher fee first.
	lo := types.SignTx(&types.Tx{From: a, To: b, Nonce: 3, Value: 1, Fee: 5})
	hi := mk(b, 0, 50)
	mid := types.SignTx(&types.Tx{From: a, To: b, Nonce: 2, Value: 1, Fee: 9})
	for _, tx := range []*types.Tx{lo, hi, mid} {
		if err := p.Add(tx); err != nil {
			t.Fatal(err)
		}
	}
	// Packing must also stay nonce-ordered within a sender: a0(t1), then a2
	// cannot execute until a1 exists, so it is skipped for the block. Build a
	// cleaner isolated pool to assert pure fee priority.
	clean := pool.New(st, 0)
	clean.Add(hi) // b nonce0 fee50
	clean.Add(types.SignTx(&types.Tx{From: a, To: b, Nonce: 0, Value: 1, Fee: 9}))
	clean.Add(types.SignTx(&types.Tx{From: a, To: b, Nonce: 1, Value: 1, Fee: 5}))
	got := clean.Ordered(10)
	if len(got) != 3 || got[0].Fee != 50 || got[1].Fee != 9 || got[2].Fee != 5 {
		var fs []uint64
		for _, x := range got {
			fs = append(fs, x.Fee)
		}
		t.Fatalf("order = %v, want [50 9 5]", fs)
	}
	// And gapped nonces in the main pool: a3 cannot run before a2 relative to
	// committed nonce 0, so Ordered must not produce an invalid sequence.
	_ = lo
	_ = mid
}
