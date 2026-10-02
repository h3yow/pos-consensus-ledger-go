package state_test

import (
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestSlashToTreasury verifies a slash moves (not burns) stake: the offender
// loses exactly what the protocol public account gains, total supply is
// unchanged, the slash is capped at the balance, and the treasury balance is
// part of the state root.
func TestSlashToTreasury(t *testing.T) {
	st := state.New()
	var offender types.Address
	offender[19] = 7
	st.SetBalance(offender, 999)

	before := st.TotalBalance()
	rootBefore := st.Root()

	moved := st.Slash(offender, 333)
	if moved != 333 {
		t.Fatalf("moved = %d want 333", moved)
	}
	if got := st.GetBalance(offender); got != 666 {
		t.Fatalf("offender = %d want 666", got)
	}
	if got := st.TreasuryBalance(); got != 333 {
		t.Fatalf("treasury = %d want 333", got)
	}
	if st.TotalBalance() != before {
		t.Fatalf("total changed by slash: before=%d after=%d", before, st.TotalBalance())
	}

	// A second slash moves more into the same single treasury account.
	moved = st.Slash(offender, 100)
	if moved != 100 || st.TreasuryBalance() != 433 || st.GetBalance(offender) != 566 {
		t.Fatalf("second slash: moved=%d treasury=%d offender=%d",
			moved, st.TreasuryBalance(), st.GetBalance(offender))
	}

	// Slash beyond balance is capped and never goes negative.
	moved = st.Slash(offender, 1_000_000)
	if moved != 566 || st.GetBalance(offender) != 0 {
		t.Fatalf("cap: moved=%d offender=%d want 566,0", moved, st.GetBalance(offender))
	}
	if st.TreasuryBalance() != 999 {
		t.Fatalf("treasury should hold whole stake 999, got %d", st.TreasuryBalance())
	}
	if st.TotalBalance() != before {
		t.Fatal("total must remain identical after capped slashes")
	}

	// Treasury is a real committed account: changing it changes the root.
	if st.Root() == rootBefore {
		t.Fatal("state root must change once the treasury is funded")
	}

	// Two independent states with identical offender/treasury balances hash
	// identically regardless of operation order.
	other := state.New()
	other.SetBalance(offender, 0)
	other.SetBalance(state.PublicTreasury, 999)
	if st.Root() != other.Root() {
		t.Fatal("identical balances (incl. treasury) must yield identical roots")
	}
	t.Logf("decision: slash moves 999 offender->treasury, total=%d unchanged, treasury committed in root", before)
}
