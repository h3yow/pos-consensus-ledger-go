package consensus

import (
	"sort"

	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// ValidatorSet is the fixed genesis validator set sorted by address.
// Weights never change: slashing moves balance to the treasury but does
// not alter consensus voting power, keeping the schedule deterministic.
type ValidatorSet struct {
	vals []*types.Validator
	by   map[types.Address]*types.Validator
}

// NewValidatorSet builds and sorts a validator set by ascending address.
func NewValidatorSet(vals []*types.Validator) *ValidatorSet {
	vs := &ValidatorSet{by: map[types.Address]*types.Validator{}}
	for _, v := range vals {
		cp := *v
		vs.vals = append(vs.vals, &cp)
		vs.by[v.Address] = &cp
	}
	sort.Slice(vs.vals, func(i, j int) bool {
		return vs.vals[i].Address.Cmp(vs.vals[j].Address) < 0
	})
	// rebuild map pointers to the sorted copies
	vs.by = map[types.Address]*types.Validator{}
	for _, v := range vs.vals {
		vs.by[v.Address] = v
	}
	return vs
}

// Get looks up a validator by address.
func (v *ValidatorSet) Get(a types.Address) (*types.Validator, bool) {
	val, ok := v.by[a]
	return val, ok
}

// Sorted returns validators ordered by ascending address.
func (v *ValidatorSet) Sorted() []*types.Validator { return v.vals }

// At returns the validator at sorted index i (cyclic).
func (v *ValidatorSet) At(i int) *types.Validator {
	return v.vals[((i%len(v.vals))+len(v.vals))%len(v.vals)]
}

// Len returns the number of validators.
func (v *ValidatorSet) Len() int { return len(v.vals) }

// TotalWeight sums all validator weights.
func (v *ValidatorSet) TotalWeight() uint64 {
	var total uint64
	for _, val := range v.vals {
		total += val.Weight
	}
	return total
}
