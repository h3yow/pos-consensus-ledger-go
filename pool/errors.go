package pool

import "errors"

// Admission / capacity errors. Every rejection reason is a distinct sentinel
// so callers and tests can tell exactly why a transaction was turned away.
var (
	// ErrAlreadyKnown is the PURE duplicate: byte-identical transaction body
	// (and therefore identical hash) already occupies the pool.
	ErrAlreadyKnown = errors.New("tx rejected: duplicate transaction")

	// ErrSameNonceConflict is a same-sender, same-nonce transaction with
	// DIFFERENT content. Only one transaction may occupy a (sender,nonce)
	// slot, and a later conflicting replacement is rejected outright rather
	// than replacing the original.
	ErrSameNonceConflict = errors.New("tx rejected: different transaction already waiting for the same sender+nonce")

	// ErrPoolFull means the pool is at capacity and the incoming transaction
	// does not outbid (or out-tie) the lowest-priority EVICTABLE occupant: no
	// eligible occupant can be evicted to make room. This also covers the case
	// where admitting the newcomer would necessarily gap a sender's nonce
	// sequence (e.g. cap=1 already holds nonce 0 and nonce 1 arrives): the
	// transaction is refused whole and the pool is left untouched.
	ErrPoolFull = errors.New("tx rejected: pool full and fee/arrival priority too low")
)
