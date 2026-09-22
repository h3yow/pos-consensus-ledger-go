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

// Slash removes slashAmount from the offender's balance and returns the
// amount actually burned (capped at the offender's remaining balance so
// balances never go negative).
func (s *State) Slash(offender types.Address, slashAmount uint64) uint64 {
	cur := s.GetBalance(offender)
	if slashAmount > cur {
		slashAmount = cur
	}
	if slashAmount == 0 {
		return 0
	}
	s.acct(offender).Balance -= slashAmount
	return slashAmount
}

// TotalBalance returns the sum of all account balances. Combined with the
// accumulated burn (fees + slashes) it must equal the genesis total.
func (s *State) TotalBalance() uint64 {
	var total uint64
	for _, a := range s.accounts {
		total += a.Balance
	}
	return total
}
