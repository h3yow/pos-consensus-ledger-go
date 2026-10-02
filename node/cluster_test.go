package node_test

import (
	"crypto/ed25519"
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

// addr(i) = identity-derived address for validator i (matches keyFor).
// addr(99) returns the funded non-validator user address (userAddr).
func addr(i byte) types.Address {
	if i == 99 {
		return userAddr(99)
	}
	a, _ := keyFor(i)
	return a
}

type cluster struct {
	t     *testing.T
	net   *simnet.Network
	nodes []*node.Node
	vals  []*types.Validator
	keys  map[types.Address]ed25519.PrivateKey
	g     *types.Block
	gs    *state.State
	logw  io.Writer
}

func newCluster(t *testing.T, n int, stakes ...uint64) *cluster {
	t.Helper()
	w := &tWriter{t: t}
	net := simnet.New()
	vals := make([]*types.Validator, n)
	keys := make(map[types.Address]ed25519.PrivateKey)
	allocs := make([]consensus.GenesisAlloc, 0, n+1)
	for i := 0; i < n; i++ {
		stake := uint64(1000)
		if i < len(stakes) {
			stake = stakes[i]
		}
		a, priv := keyFor(byte(i + 1))
		keys[a] = priv
		vals[i] = &types.Validator{
			Addr: a, Stake: stake,
			PubKey: priv.Public().(ed25519.PublicKey),
		}
		allocs = append(allocs, consensus.GenesisAlloc{Addr: a, Balance: stake})
	}
	// a non-validator funded user (address derived from its own deterministic
	// key; transactions use the simulation tx signature)
	user := userAddr(99)
	allocs = append(allocs, consensus.GenesisAlloc{Addr: user, Balance: 5000})
	g, gs := consensus.MakeGenesis(vals, allocs)

	nodes := make([]*node.Node, n)
	for i := range nodes {
		nodes[i] = node.New(node.Config{
			Name:         nameOf(i),
			Addr:         vals[i].Addr,
			PrivKey:      keys[vals[i].Addr],
			Net:          net,
			Vals:         vals,
			Genesis:      g,
			GenesisState: gs,
			Logger:       log.New(w, "", 0),
		})
	}
	return &cluster{t: t, net: net, nodes: nodes, vals: vals, keys: keys, g: g, gs: gs, logw: w}
}

// keyFor derives validator identity i deterministically.
func keyFor(i byte) (types.Address, ed25519.PrivateKey) {
	priv := types.GenerateKey([]byte{0x76, 'v', 'a', 'l', i})
	pub := priv.Public().(ed25519.PublicKey)
	return types.AddressOf(pub), priv
}

// userAddr derives a non-validator user address.
func userAddr(i byte) types.Address {
	priv := types.GenerateKey([]byte{0x75, 'u', 's', 'r', i})
	return types.AddressOf(priv.Public().(ed25519.PublicKey))
}

// signVote signs a consensus vote with the private key bound to voter; tests
// use it both for honest votes and (with the offender's key) to construct
// authentic equivocation — no honest key is ever used to forge another
// validator's vote.
func (c *cluster) signVote(v *types.Vote) *types.Vote {
	priv, ok := c.keys[v.Voter]
	if !ok {
		c.t.Fatalf("no private key for voter %x", v.Voter[:8])
	}
	return types.SignVote(v, priv)
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
