package pool

import (
	"sync"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// TestEvictionNeverGapsSenderConcurrent is a white-box stress test: many
// senders submit concurrently into a small capped pool, and EVERY admission
// is checked atomically (under the pool lock) for the gap rule — if the
// admission evicted an occupant, the victim's nonce must be the maximum of
// its sender's waiting nonces COUNTING the incoming transaction. Removing
// anything lower would leave a larger nonce behind while dropping a smaller
// one it still needs.
func TestEvictionNeverGapsSenderConcurrent(t *testing.T) {
	st := state.New()
	const senders = 6
	addrs := make([]types.Address, senders)
	for i := range addrs {
		addrs[i][19] = byte(40 + i)
		st.SetBalance(addrs[i], 1_000_000)
	}
	const cap = 40
	p := New(st, cap)

	snapshot := func() map[types.Address]map[uint64]types.Hash {
		m := map[types.Address]map[uint64]types.Hash{}
		for h, e := range p.pending {
			if m[e.Tx.From] == nil {
				m[e.Tx.From] = map[uint64]types.Hash{}
			}
			m[e.Tx.From][e.Tx.Nonce] = h
		}
		return m
	}

	var wg sync.WaitGroup
	const per = 30
	for si := range addrs {
		from := addrs[si]
		for n := 0; n < per; n++ {
			wg.Add(1)
			go func(nonce int) {
				defer wg.Done()
				tx := types.SignTx(&types.Tx{From: from, To: addrs[0],
					Nonce: uint64(nonce), Value: 1, Fee: uint64(nonce % 13)})

				p.mu.Lock()
				defer p.mu.Unlock()
				before := snapshot()
				beforeLen := len(p.pending)
				err := p.addLocked(tx)
				if err != nil {
					// A refusal must not change the pool at all.
					if len(p.pending) != beforeLen {
						t.Errorf("refused add changed pool size %d -> %d", beforeLen, len(p.pending))
					}
					return
				}
				if beforeLen < p.cap {
					return // no eviction was needed
				}
				// Exactly one occupant must have been evicted: find it.
				after := snapshot()
				var victimFrom types.Address
				var victimNonce uint64
				found := false
				for addr, set := range before {
					for nn, h := range set {
						if _, ok := p.pending[h]; !ok {
							if found {
								t.Errorf("more than one eviction in a single add")
							}
							found, victimFrom, victimNonce = true, addr, nn
						}
					}
				}
				if !found {
					t.Errorf("pool was full and tx admitted, but no occupant was evicted")
					return
				}
				// The victim must be the max waiting nonce of its sender,
				// counting the incoming tx.
				maxN := uint64(0)
				if victimFrom == tx.From && tx.Nonce > maxN {
					maxN = tx.Nonce
				}
				for nn := range before[victimFrom] {
					if nn > maxN {
						maxN = nn
					}
				}
				if victimNonce != maxN {
					t.Errorf("GAP: evicted nonce %d of sender %x while %d still waits (incoming nonce %d)",
						victimNonce, victimFrom[:4], maxN, tx.Nonce)
				}
				_ = after
			}(n)
		}
	}
	wg.Wait()
	if p.Size() > cap {
		t.Fatalf("capacity exceeded: %d > %d", p.Size(), cap)
	}
	t.Logf("decision: %d concurrent senders x %d txs into cap=%d: every eviction removed only a per-sender tail (counting the newcomer); size=%d",
		senders, per, cap, p.Size())
}
