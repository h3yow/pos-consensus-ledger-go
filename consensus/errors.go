package consensus

import "errors"

// Block / vote validation errors.
var (
	ErrUnknownParent   = errors.New("block rejected: unknown parent")
	ErrWrongProposer   = errors.New("block rejected: proposer is not scheduled at this height")
	ErrBadProposerSig  = errors.New("block rejected: proposer signature missing or invalid")
	ErrBadStateRoot    = errors.New("block rejected: state root mismatch")
	ErrDuplicateBlock  = errors.New("block rejected: duplicate block")
	ErrJustification   = errors.New("block rejected: parent justification votes insufficient")
	ErrFinalizedReorg  = errors.New("block rejected: conflicts with finalized chain")
	ErrBadVote         = errors.New("vote rejected: unknown voter or bad signature")
	ErrDoubleVote      = errors.New("vote rejected: equivocation (double vote) detected")
	ErrEvidenceInvalid = errors.New("evidence rejected: forged, tampered or mismatched")
)
