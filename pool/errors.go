package pool

import "errors"

// ErrAlreadyKnown marks a duplicate transaction.
var ErrAlreadyKnown = errors.New("tx rejected: duplicate transaction")
