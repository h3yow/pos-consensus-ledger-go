package state

import "errors"

// Distinguishable transaction rejection reasons.
var (
	ErrBadSignature = errors.New("tx rejected: invalid signature")
	ErrInsufficient = errors.New("tx rejected: insufficient balance for value+fee")
	ErrNonceTooLow  = errors.New("tx rejected: nonce too low (replay)")
	ErrNonceTooHigh = errors.New("tx rejected: nonce too high (gap/out of order)")
	ErrOverflows    = errors.New("tx rejected: value+fee overflows")
)
