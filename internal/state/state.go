// Package state implements the account state machine: balances, nonces,
// deterministic transaction application, equivocation slashing and the
// state commitment (state root).
package state

import (
	"errors"

	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Distinguishable rejection reasons for transactions.
var (
	ErrBadSignature  = errors.New("tx: invalid signature")
	ErrUnknownSender = errors.New("tx: sender account does not exist")
	ErrBadNonce      = errors.New("tx: nonce mismatch (out of order)")
	ErrInsufficient  = errors.New("tx: insufficient balance for amount+fee")
	ErrOverflow      = errors.New("tx: balance overflow")
	ErrZeroAmount    = errors.New("tx: zero amount and zero fee")
)

// Account is the per-address mutable state.
type Account struct {
	Nonce   uint64
	Balance uint64
}

// State is a copyable account database rooted by a commitment.
type State struct {
	chainID  string
	accounts map[types.Address]*Account
}

// New creates an empty state for the given chain id.
func New(chainID string) *State { panic("not implemented") }

// FromGenesis builds the genesis account set.
func FromGenesis(g *types.Genesis) *State { panic("not implemented") }

// Clone deep-copies the state so block branches can diverge.
func (s *State) Clone() *State { panic("not implemented") }

// Balance returns the balance of an address (0 if absent).
func (s *State) Balance(a types.Address) uint64 { panic("not implemented") }

// Nonce returns the nonce of an address (0 if absent).
func (s *State) Nonce(a types.Address) uint64 { panic("not implemented") }

// Accounts returns a snapshot copy of all accounts (for inspection/tests).
func (s *State) Accounts() map[types.Address]Account { panic("not implemented") }

// TotalBalance sums every account balance (conservation checks).
func (s *State) TotalBalance() uint64 { panic("not implemented") }

// Validate checks a transaction against this state without mutating it.
func (s *State) Validate(tx *types.Tx) error { panic("not implemented") }

// Apply validates and executes a transaction: amount to recipient, fee to sink.
func (s *State) Apply(tx *types.Tx, feeSink types.Address) error { panic("not implemented") }

// Slash removes stake from an offender and credits the treasury.
// Returns the slashed amount. Total balance is conserved.
func (s *State) Slash(offender, treasury types.Address, num, denom uint64) uint64 {
	panic("not implemented")
}

// Root computes the deterministic state commitment over all accounts.
func (s *State) Root() types.Hash { panic("not implemented") }
