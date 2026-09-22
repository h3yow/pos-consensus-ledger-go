// Package state implements the account world-state and the state-root commitment.
package state

import (
	"bytes"
	"sort"

	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// Account holds a single account's balance and transaction nonce.
type Account struct {
	Balance uint64
	Nonce   uint64
}

// State is the mutable world state backed by an in-memory map.
type State struct {
	accounts map[types.Address]*Account
}

// New returns an empty state.
func New() *State {
	return &State{accounts: make(map[types.Address]*Account)}
}

func (s *State) acct(addr types.Address) *Account {
	a := s.accounts[addr]
	if a == nil {
		a = &Account{}
		s.accounts[addr] = a
	}
	return a
}

// GetBalance returns 0 for unknown accounts.
func (s *State) GetBalance(addr types.Address) uint64 {
	if a := s.accounts[addr]; a != nil {
		return a.Balance
	}
	return 0
}

// GetNonce returns 0 for unknown accounts.
func (s *State) GetNonce(addr types.Address) uint64 {
	if a := s.accounts[addr]; a != nil {
		return a.Nonce
	}
	return 0
}

// SetBalance overwrites an account balance.
func (s *State) SetBalance(addr types.Address, balance uint64) {
	s.acct(addr).Balance = balance
}

// setNonce is used during transaction execution.
func (s *State) setNonce(addr types.Address, nonce uint64) {
	s.acct(addr).Nonce = nonce
}

// Root returns the deterministic state commitment: SHA-256 over accounts
// sorted by address, each encoded as (nonce, balance). Identical state on
// every node therefore yields an identical root.
func (s *State) Root() types.Hash {
	addrs := make([]types.Address, 0, len(s.accounts))
	for a := range s.accounts {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})

	var buf bytes.Buffer
	var nb [16]byte
	for _, a := range addrs {
		acc := s.accounts[a]
		// skip never-touched zero accounts to keep the commitment minimal
		if acc.Balance == 0 && acc.Nonce == 0 {
			continue
		}
		buf.Write(a[:])
		putU64(nb[:8], acc.Nonce)
		putU64(nb[8:], acc.Balance)
		buf.Write(nb[:])
	}
	return types.HashBytes(buf.Bytes())
}

func putU64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

// Clone deep-copies the state (used for speculative block execution).
func (s *State) Clone() *State {
	c := New()
	for a, acc := range s.accounts {
		cp := *acc
		c.accounts[a] = &cp
	}
	return c
}
