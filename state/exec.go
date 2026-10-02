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
// fee is burned per the protocol's existing convention (conservation is
// auditable via TotalBalance plus a fee-burn counter held by the executor).
// Slashed stake is never burned: it lands in ProtocolAccount.
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

// ProtocolAccount is the network-wide, agreement-level public account that
// receives every slash. It is the zero address: a protocol-level account no
// private key can spend from. Slashed stake is moved there (not removed from
// supply), so at every height and on every node:
//
//	sum(all balances, including ProtocolAccount) + burned fees == genesis total
//
// Because it is a regular state account, its balance is part of the state
// commitment (state.Root), so all nodes commit to the same destination and
// amount for every slash.
var ProtocolAccount types.Address

// Slash removes slashAmount from the offender's balance and CREDITS the same
// amount to the protocol public account. Nothing is destroyed by slashing.
// The amount is capped at the offender's remaining balance so balances never
// go negative; the actually moved amount is returned.
func (s *State) Slash(offender types.Address, slashAmount uint64) uint64 {
	cur := s.GetBalance(offender)
	if slashAmount > cur {
		slashAmount = cur
	}
	if slashAmount == 0 {
		return 0
	}
	s.acct(offender).Balance -= slashAmount
	s.acct(ProtocolAccount).Balance += slashAmount
	return slashAmount
}

// TotalBalance returns the sum of every account balance, including the
// protocol public account. Combined with accumulated burned fees it must
// equal the genesis total.
func (s *State) TotalBalance() uint64 {
	var total uint64
	for _, a := range s.accounts {
		total += a.Balance
	}
	return total
}
