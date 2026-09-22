// Package state implements the account state machine: balances, nonces,
// deterministic transaction application, equivocation slashing and the
// state commitment (state root).
//
// State is a pure, copyable data structure: block branches fork by
// cloning the parent state, and every node applying the same block gets
// byte-identical state roots.
package state

import (
	"errors"
	"sort"

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
func New(chainID string) *State {
	return &State{chainID: chainID, accounts: map[types.Address]*Account{}}
}

// FromGenesis builds the genesis account set.
func FromGenesis(g *types.Genesis) *State {
	s := New(g.ChainID)
	for addr, bal := range g.Balances {
		a := addr // local copy
		s.accounts[a] = &Account{Balance: bal}
	}
	return s
}

// Clone deep-copies the state so block branches can diverge.
func (s *State) Clone() *State {
	c := &State{chainID: s.chainID, accounts: make(map[types.Address]*Account, len(s.accounts))}
	for addr, acc := range s.accounts {
		a := addr
		ac := *acc
		c.accounts[a] = &ac
	}
	return c
}

// Balance returns the balance of an address (0 if absent).
func (s *State) Balance(a types.Address) uint64 {
	if acc, ok := s.accounts[a]; ok {
		return acc.Balance
	}
	return 0
}

// Nonce returns the nonce of an address (0 if absent).
func (s *State) Nonce(a types.Address) uint64 {
	if acc, ok := s.accounts[a]; ok {
		return acc.Nonce
	}
	return 0
}

// Accounts returns a snapshot copy of all accounts (for inspection/tests).
func (s *State) Accounts() map[types.Address]Account {
	out := make(map[types.Address]Account, len(s.accounts))
	for addr, acc := range s.accounts {
		out[addr] = *acc
	}
	return out
}

// TotalBalance sums every account balance (conservation checks).
func (s *State) TotalBalance() uint64 {
	var total uint64
	for _, acc := range s.accounts {
		total += acc.Balance
	}
	return total
}

// Validate checks a transaction against this state without mutating it.
// The checks happen in a fixed order so rejection reasons are stable.
func (s *State) Validate(tx *types.Tx) error {
	if !tx.VerifySig() {
		return ErrBadSignature
	}
	from := tx.Sender()
	sender, ok := s.accounts[from]
	if !ok {
		return ErrUnknownSender
	}
	if tx.Amount == 0 && tx.Fee == 0 {
		return ErrZeroAmount
	}
	if tx.Nonce != sender.Nonce {
		return ErrBadNonce
	}
	need, overflow := addOverflow(tx.Amount, tx.Fee)
	if overflow {
		return ErrOverflow
	}
	if sender.Balance < need {
		return ErrInsufficient
	}
	return nil
}

// Apply validates and executes a transaction: the amount goes to the
// recipient and the fee to the fee sink. The sender nonce advances by one.
func (s *State) Apply(tx *types.Tx, feeSink types.Address) error {
	if err := s.Validate(tx); err != nil {
		return err
	}
	from := tx.Sender()
	sender := s.accounts[from]

	need := tx.Amount + tx.Fee
	sender.Balance -= need
	sender.Nonce++

	recipient := s.account(tx.To)
	if b, overflow := addOverflow(recipient.Balance, tx.Amount); overflow {
		return ErrOverflow
	} else {
		recipient.Balance = b
	}

	if tx.Fee > 0 {
		sink := s.account(feeSink)
		if b, overflow := addOverflow(sink.Balance, tx.Fee); overflow {
			return ErrOverflow
		} else {
			sink.Balance = b
		}
	}
	return nil
}

// account returns the account for addr, creating an empty one on first use.
func (s *State) account(addr types.Address) *Account {
	if acc, ok := s.accounts[addr]; ok {
		return acc
	}
	acc := &Account{}
	s.accounts[addr] = acc
	return acc
}

// Slash removes stake from an offender's balance and credits the same
// amount to the treasury. It returns the slashed amount. If the offender
// has no balance the slash is a no-op. Total balance is conserved.
func (s *State) Slash(offender, treasury types.Address, num, denom uint64) uint64 {
	if denom == 0 || num == 0 {
		return 0
	}
	victim, ok := s.accounts[offender]
	if !ok || victim.Balance == 0 {
		return 0
	}
	amount := victim.Balance * num / denom
	if amount == 0 {
		return 0
	}
	victim.Balance -= amount
	dest := s.account(treasury)
	dest.Balance += amount
	return amount
}

// Root computes the deterministic state commitment:
// root = Merkle over per-account leaves ordered by ascending address,
// where leaf = hash(addr || nonce || balance).
func (s *State) Root() types.Hash {
	addrs := make([]types.Address, 0, len(s.accounts))
	for addr := range s.accounts {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Cmp(addrs[j]) < 0 })

	leaves := make([]types.Hash, 0, len(addrs))
	for _, addr := range addrs {
		acc := s.accounts[addr]
		b := []byte("account/v1:")
		b = types.PutAddr(b, addr)
		b = types.PutUint64(b, acc.Nonce)
		b = types.PutUint64(b, acc.Balance)
		leaves = append(leaves, types.HashFromBytes(b))
	}
	return merkleRoot(leaves)
}

// merkleRoot is an iterative binary Merkle root over ordered leaves.
// Odd nodes are promoted (duplicated) to the next level.
func merkleRoot(leaves []types.Hash) types.Hash {
	if len(leaves) == 0 {
		return types.HashFromBytes([]byte("emptytree/v1"))
	}
	level := leaves
	for len(level) > 1 {
		var next []types.Hash
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				// Promote the odd element (equivalent to duplicating it,
				// but the preimage is tagged to avoid ambiguity).
				b := []byte("merkle/odd:")
				b = types.PutHash(b, level[i])
				next = append(next, types.HashFromBytes(b))
				continue
			}
			b := []byte("merkle/node:")
			b = types.PutHash(b, level[i])
			b = types.PutHash(b, level[i+1])
			next = append(next, types.HashFromBytes(b))
		}
		level = next
	}
	return level[0]
}

// addOverflow returns the sum and whether it overflowed uint64.
func addOverflow(a, b uint64) (uint64, bool) {
	sum := a + b
	if sum < a {
		return 0, true
	}
	return sum, false
}
