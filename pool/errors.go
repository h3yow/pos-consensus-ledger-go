package pool

import "errors"

// Admission / capacity errors, all distinguishable from one another and from
// the state-level execution errors.
var (
	// ErrAlreadyKnown marks a byte-identical duplicate transaction.
	ErrAlreadyKnown = errors.New("tx rejected: duplicate transaction")
	// ErrSameNonceConflict marks a different transaction from the same sender
	// for a nonce already occupied in the pool (nonce-slot replacement is not
	// allowed; the incumbent wins).
	ErrSameNonceConflict = errors.New("tx rejected: same sender+nonce already occupied by a different tx")
	// ErrPoolFull marks rejection under capacity pressure: the pool is full
	// and the newcomer loses the priority comparison against every slot it
	// would have to displace (lower fee, or equal fee and later arrival).
	ErrPoolFull = errors.New("tx rejected: pool full, insufficient priority to displace")
)
