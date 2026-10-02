package consensus

import "errors"

// Block / vote validation errors.
var (
	ErrUnknownParent   = errors.New("block rejected: unknown parent")
	ErrWrongProposer   = errors.New("block rejected: proposer is not scheduled at this height")
	ErrBadStateRoot    = errors.New("block rejected: state root mismatch")
	ErrDuplicateBlock  = errors.New("block rejected: duplicate block")
	ErrJustification   = errors.New("block rejected: parent justification votes insufficient")
	ErrFinalizedReorg  = errors.New("block rejected: conflicts with finalized chain")
	ErrBadProposerSig  = errors.New("block rejected: proposer signature invalid")
	ErrBadVote         = errors.New("vote rejected: unknown voter or invalid signature")
	ErrDoubleVote      = errors.New("vote rejected: equivocation (double vote) detected")
	ErrEvidenceInvalid = errors.New("evidence rejected: invalid or mismatched")
)
