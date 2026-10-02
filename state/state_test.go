package state_test

import (
	"errors"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

var (
	from = types.Address{}
	to   = types.Address{19: 9}
)

func mkTx(nonce, value, fee uint64) *types.Tx {
	return types.SignTx(&types.Tx{From: from, To: to, Nonce: nonce, Value: value, Fee: fee})
}

// TestRejectReasons verifies each invalid/conflicting/out-of-order tx maps to
// a distinct, comparable error.
func TestRejectReasons(t *testing.T) {
	st := state.New()
	st.SetBalance(from, 100)

	cases := []struct {
		name string
		tx   *types.Tx
		mut  func(*types.Tx)
		want error
	}{
		{"bad signature", mkTx(0, 1, 1), func(t *types.Tx) { t.Sig[0] ^= 0xFF }, state.ErrBadSignature},
		{"missing signature", mkTx(0, 1, 1), func(t *types.Tx) { t.Sig = nil }, state.ErrBadSignature},
		{"nonce too high/gap", mkTx(5, 1, 1), nil, state.ErrNonceTooHigh},
		{"nonce too high/gap", mkTx(1, 1, 1), nil, state.ErrNonceTooHigh},
		{"insufficient funds", mkTx(0, 200, 0), nil, state.ErrInsufficient},
		{"value+fee insufficient", mkTx(0, 100, 1), nil, state.ErrInsufficient},
		{"overflow", func() *types.Tx {
			t := types.SignTx(&types.Tx{From: from, To: to, Nonce: 0, Value: ^uint64(0), Fee: 2})
			return t
		}(), nil, state.ErrOverflows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// fresh state per case so nonce/balance assumptions hold
			loc := state.New()
			loc.SetBalance(from, 100)
			tx := *tc.tx
			if tc.mut != nil {
				tc.mut(&tx)
			}
			err := loc.ApplyTx(&tx)
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: want %v, got %v", tc.name, tc.want, err)
			}
		})
	}

	// A valid ordered sequence executes and advances nonce/balances.
	for _, n := range []uint64{0, 1, 2} {
		if err := st.ApplyTx(mkTx(n, 10, 1)); err != nil {
			t.Fatalf("nonce %d: %v", n, err)
		}
	}
	if st.GetNonce(from) != 3 {
		t.Fatalf("nonce = %d, want 3", st.GetNonce(from))
	}
	// replay of an old nonce rejected distinctly
	if err := st.ApplyTx(mkTx(1, 1, 1)); !errors.Is(err, state.ErrNonceTooLow) {
		t.Fatalf("replay: want ErrNonceTooLow, got %v", err)
	}
	// conservation: sender paid 33, receiver got 30, 3 fee burned
	if st.TotalBalance() != 97 {
		t.Fatalf("total = %d, want 97 (3 fee burned)", st.TotalBalance())
	}
}

// TestStateRootDeterminism checks root stability and content addressing.
func TestStateRootDeterminism(t *testing.T) {
	a := state.New()
	b := state.New()
	for _, st := range []*state.State{a, b} {
		st.SetBalance(from, 500)
		st.SetBalance(to, 250)
	}
	if a.Root() != b.Root() {
		t.Fatal("identical states must hash identically regardless of insertion")
	}
	b.SetBalance(to, 251)
	if a.Root() == b.Root() {
		t.Fatal("different states must hash differently")
	}
}

// TestSlashToProtocolAccount verifies slashing MOVES stake into the protocol
// public account (supply is preserved), that the move changes the committed
// state root, and that two nodes executing the same slash commit identically.
func TestSlashToProtocolAccount(t *testing.T) {
	var offender types.Address
	offender[19] = 3
	a := state.New()
	a.SetBalance(offender, 1000)

	before := a.TotalBalance()
	rootBefore := a.Root()
	moved := a.Slash(offender, 300)
	if moved != 300 {
		t.Fatalf("moved = %d, want 300", moved)
	}
	if got := a.GetBalance(offender); got != 700 {
		t.Fatalf("offender = %d, want 700", got)
	}
	if got := a.GetBalance(state.ProtocolAccount); got != 300 {
		t.Fatalf("protocol account = %d, want 300", got)
	}
	if a.TotalBalance() != before {
		t.Fatalf("supply changed by slashing: before=%d after=%d", before, a.TotalBalance())
	}
	if a.Root() == rootBefore {
		t.Fatal("slashing into the public account must change the state root")
	}

	// Cap at remaining balance; independent node commits the same root.
	a.Slash(offender, 5000)
	if a.GetBalance(offender) != 0 {
		t.Fatal("slash must cap at the offender's remaining balance")
	}
	b := state.New()
	b.SetBalance(offender, 1000)
	b.Slash(offender, 300)
	b.Slash(offender, 700)
	if a.Root() != b.Root() {
		t.Fatal("identical slash sequences must commit identical state roots")
	}
}
