package consensus

import "github.com/h3yow/pos-consensus-ledger-go/internal/types"

// ValidatorSet is the fixed genesis validator set sorted by address.
type ValidatorSet struct {
	vals []*types.Validator
	by   map[types.Address]*types.Validator
}

// NewValidatorSet builds and sorts a validator set.
func NewValidatorSet(vals []*types.Validator) *ValidatorSet { panic("not implemented") }

// Get looks up a validator by address.
func (v *ValidatorSet) Get(a types.Address) (*types.Validator, bool) { panic("not implemented") }

// Sorted returns validators ordered by ascending address.
func (v *ValidatorSet) Sorted() []*types.Validator { panic("not implemented") }

// TotalWeight sums all validator weights.
func (v *ValidatorSet) TotalWeight() uint64 { panic("not implemented") }
