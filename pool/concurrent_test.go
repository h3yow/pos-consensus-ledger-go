package pool_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestPoolConcurrentStress hammers one capped pool from many goroutines. The
// pool is used under a single node's event loop in production, but admission
// must remain safe and predictable under sustained concurrent pressure:
// capacity is never exceeded, each (sender,nonce) slot holds at most one tx,
// and only the documented sentinel errors ever come back.
func TestPoolConcurrentStress(t *testing.T) {
	st := state.New()
	const senders = 8
	addrs := make([]types.Address, senders)
	for i := range addrs {
		addrs[i] = mkAddr(byte(20 + i))
		st.SetBalance(addrs[i], 1_000_000)
	}
	const cap = 50
	p := pool.New(st, cap)

	var wg sync.WaitGroup
	const per = 40
	for si := range addrs {
		from := addrs[si]
		for n := 0; n < per; n++ {
			wg.Add(1)
			go func(nonce int) {
				defer wg.Done()
				tx := tx(from, addrs[0], uint64(nonce), 1, uint64(nonce%17))
				err := p.Add(tx)
				switch {
				case err == nil,
					errors.Is(err, pool.ErrPoolFull),
					errors.Is(err, pool.ErrAlreadyKnown),
					errors.Is(err, pool.ErrSameNonceConflict):
				default:
					t.Errorf("unexpected admission error: %v", err)
				}
			}(n)
		}
	}
	wg.Wait()

	if p.Size() > cap {
		t.Fatalf("pool exceeded capacity: %d > %d", p.Size(), cap)
	}
	// One tx per (sender,nonce): scan pending entries for duplicate slots.
	seen := map[[28]byte]bool{}
	for _, e := range p.Pending() {
		var k [28]byte
		copy(k[:20], e.Tx.From[:])
		for i := 0; i < 8; i++ {
			k[20+i] = byte(e.Tx.Nonce >> (8 * i))
		}
		if seen[k] {
			t.Fatal("two different transactions occupy one (sender,nonce) slot")
		}
		seen[k] = true
	}
	t.Logf("decision: after concurrent stress size=%d <= cap=%d, slots unique, only documented errors", p.Size(), cap)
}
