package node_test

import (
	"io"
	"log"
	"sync"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// tWriter serializes test log writes and forwards them to t.Logf so every
// input message and decision basis is visible when tests run with -v.
type tWriter struct {
	mu sync.Mutex
	t  *testing.T
}

func (w *tWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t.Logf("%s", p)
	return len(p), nil
}

// addr(i) = [0..0,i], a deterministic test identity.
func addr(i byte) types.Address {
	var a types.Address
	a[19] = i
	return a
}

type cluster struct {
	t     *testing.T
	net   *simnet.Network
	nodes []*node.Node
	vals  []*types.Validator
	g     *types.Block
	gs    *state.State
	logw  io.Writer
}

func newCluster(t *testing.T, n int, stakes ...uint64) *cluster {
	t.Helper()
	w := &tWriter{t: t}
	net := simnet.New()
	vals := make([]*types.Validator, n)
	allocs := make([]consensus.GenesisAlloc, 0, n+1)
	for i := 0; i < n; i++ {
		stake := uint64(1000)
		if i < len(stakes) {
			stake = stakes[i]
		}
		vals[i] = &types.Validator{Addr: addr(byte(i + 1)), Stake: stake}
		allocs = append(allocs, consensus.GenesisAlloc{Addr: addr(byte(i + 1)), Balance: stake})
	}
	// a non-validator funded user
	allocs = append(allocs, consensus.GenesisAlloc{Addr: addr(99), Balance: 5000})
	g, gs := consensus.MakeGenesis(vals, allocs)

	nodes := make([]*node.Node, n)
	for i := range nodes {
		nodes[i] = node.New(node.Config{
			Name:         nameOf(i),
			Addr:         addr(byte(i + 1)),
			Net:          net,
			Vals:         vals,
			Genesis:      g,
			GenesisState: gs,
			Logger:       log.New(w, "", 0),
		})
	}
	return &cluster{t: t, net: net, nodes: nodes, vals: vals, g: g, gs: gs, logw: w}
}

func nameOf(i int) string {
	return string(rune('A' + i))
}

// proposerIndex finds which node is scheduled at height h.
func (c *cluster) proposerIndex(h uint64) int {
	want := consensus.NewSet(c.vals).ProposerAt(h)
	for i, nd := range c.nodes {
		if nd.Addr == want {
			return i
		}
	}
	return -1
}

// step produces one block, delivers it, then collects and delivers votes.
func (c *cluster) step() *types.Block {
	h := c.nodes[0].Head().Height + 1
	pi := c.proposerIndex(h)
	b := c.nodes[pi].ProduceBlock()
	if b == nil {
		c.t.Fatalf("no proposer produced at height %d", h)
	}
	c.net.DeliverAll()
	for _, nd := range c.nodes {
		nd.CastVote()
	}
	c.net.DeliverAll()
	return b
}

// assertSameHead verifies every node has identical canonical head hash.
func (c *cluster) assertSameHead() types.Hash {
	var hh types.Hash
	for i, nd := range c.nodes {
		h := types.HashBlock(nd.Head())
		if i == 0 {
			hh = h
			continue
		}
		if h != hh {
			c.t.Fatalf("node %s head %x != %x", nd.Name, h[:8], hh[:8])
		}
	}
	return hh
}
