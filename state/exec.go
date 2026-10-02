package state

import (
	"math"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// ApplyTx validates and executes a transaction against the state.
// Checks happen before any mutation, and failures return distinguishable
// errors:
//
//	ErrBadSignature -> wrong/absent signature
//	ErrOverflows    -> value+fee wraps uint64
//	ErrNonceTooLow  -> replay (nonce below account nonce)
//	ErrNonceTooHigh -> gap (nonce above account nonce)
//	ErrInsufficient -> balance cannot cover value+fee
//
// On success the sender pays value+fee, the receiver gains value and the
// fee is burned (conservation is auditable via TotalBalance plus a burn
// counter held by the block executor).
func (s *State) ApplyTx(t *types.Tx) error {
	if !types.VerifyTx(t) {
		return ErrBadSignature
	}
	if t.Value > math.MaxUint64-t.Fee {
		return ErrOverflows
	}
	need := t.Value + t.Fee

	cur := s.GetNonce(t.From)
	switch {
	case t.Nonce < cur:
		return ErrNonceTooLow
	case t.Nonce > cur:
		return ErrNonceTooHigh
	}

	if s.GetBalance(t.From) < need {
		return ErrInsufficient
	}

	s.acct(t.From).Balance -= need
	s.acct(t.From).Nonce++
	s.acct(t.To).Balance += t.Value
	// fee intentionally not credited anywhere: it is burned.
	return nil
}

// PublicTreasury is the single protocol-level account that receives every
// slashing penalty. It is a fixed well-known address identical on every
// node, so all nodes credit the same destination and agree on its balance
// through the state commitment. The address is outside the validator/user
// address space used by the simulation.
var PublicTreasury = types.Address{
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff,
}

// Slash removes slashAmount from the offender's balance and transfers it to
// PublicTreasury, returning the amount actually moved (capped at the
// offender's remaining balance so balances never go negative).
//
// Nothing is destroyed by a slash: it moves stake from the offender to the
// network-wide public account. Conservation therefore stays exact, the
// treasury balance is part of the state root, and the offender's live
// balance — which is also its voting weight — drops by the same amount.
func (s *State) Slash(offender types.Address, slashAmount uint64) uint64 {
	cur := s.GetBalance(offender)
	if slashAmount > cur {
		slashAmount = cur
	}
	if slashAmount == 0 {
		return 0
	}
	s.acct(offender).Balance -= slashAmount
	s.acct(PublicTreasury).Balance += slashAmount
	return slashAmount
}

// TreasuryBalance returns the balance held by the protocol public account.
func (s *State) TreasuryBalance() uint64 {
	return s.GetBalance(PublicTreasury)
}

// TotalBalance returns the sum of all account balances, including the public
// treasury. Combined with the accumulated fee burn it must equal the genesis
// total: slashes only move value, fees are the only quantity destroyed.
func (s *State) TotalBalance() uint64 {
	var total uint64
	for _, a := range s.accounts {
		total += a.Balance
	}
	return total
}
