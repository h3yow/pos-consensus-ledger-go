// Package testkit builds local multi-node test clusters deterministically:
// fixed validator keys, a shared genesis, signing helpers for transactions,
// and an explicit (non-timed) network pumping driver.
package testkit

import (
	"crypto/ed25519"
	"fmt"
	"log"
	"os"

	"github.com/h3yow/pos-consensus-ledger-go/internal/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/internal/network"
	"github.com/h3yow/pos-consensus-ledger-go/internal/node"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Cluster is a set of nodes sharing one simulated network.
type Cluster struct {
	Engine       *consensus.Engine
	Genesis      *types.Genesis
	GenesisBlock *types.Block
	Net          *network.SimNetwork
	Keys         []ed25519.PrivateKey
	Nodes        []*node.Node
	NodeByID     map[string]*node.Node
	NodeByAddr   map[types.Address]*node.Node
}

// NewCluster builds n validators with fixed weights over a deterministic
// key seed and registers them on one SimNetwork.
func NewCluster(weights []uint64, verbose bool) (*Cluster, error) {
	keys := make([]ed25519.PrivateKey, len(weights))
	vals := make([]*types.Validator, len(weights))
	balances := map[types.Address]uint64{}
	for i, w := range weights {
		pub, priv, err := ed25519.GenerateKey(newSeedReader(int64(i + 1)))
		if err != nil {
			return nil, err
		}
		keys[i] = priv
		addr := types.AddressFromPub(pub)
		vals[i] = &types.Validator{Address: addr, PubKey: pub, Weight: w}
		// Initial balance covers stake plus spending funds.
		balances[addr] = w*10 + 1000
	}
	treasuryPub, treasuryPriv, err := ed25519.GenerateKey(newSeedReader(9999))
	if err != nil {
		return nil, err
	}
	_ = treasuryPriv
	treasury := types.AddressFromPub(treasuryPub)
	balances[treasury] = 0

	g := &types.Genesis{
		ChainID:              "sim-pos-1",
		Balances:             balances,
		Validators:           vals,
		Treasury:             treasury,
		SlashRateNumerator:   10,
		SlashRateDenominator: 100, // 10% of offender balance
		ThresholdNumerator:   2,
		ThresholdDenominator: 3, // strictly more than 2/3 of stake
	}
	eng, err := consensus.NewEngine(g)
	if err != nil {
		return nil, err
	}
	keyByAddr := map[types.Address]ed25519.PrivateKey{}
	for i, v := range vals {
		keyByAddr[v.Address] = keys[i]
	}
	firstSorted := eng.Validators().Sorted()[0]
	genKey := keyByAddr[firstSorted.Address]
	genBlock := eng.MakeGenesisBlock(func(pub []byte, msg []byte) []byte {
		return ed25519.Sign(genKey, msg)
	})

	net := network.New()
	c := &Cluster{
		Engine:       eng,
		Genesis:      g,
		GenesisBlock: genBlock,
		Net:          net,
		Keys:         keys,
		NodeByID:     map[string]*node.Node{},
		NodeByAddr:   map[types.Address]*node.Node{},
	}
	for i, v := range vals {
		var logger node.Logger
		id := fmt.Sprintf("n%d", i+1)
		if verbose {
			logger = log.New(os.Stdout, "["+id+"] ", log.LstdFlags|log.Lmicroseconds)
		} else {
			logger = nopLogger{}
		}
		n, err := node.New(node.Config{
			ID:           id,
			Key:          keys[i],
			Engine:       eng,
			GenesisBlock: genBlock,
			Net:          net,
			Logger:       logger,
		})
		if err != nil {
			return nil, err
		}
		c.Nodes = append(c.Nodes, n)
		c.NodeByID[id] = n
		c.NodeByAddr[v.Address] = n
	}
	return c, nil
}

// Validator returns the sorted validator descriptor at index i.
func (c *Cluster) Validator(i int) *types.Validator {
	return c.Engine.Validators().Sorted()[i]
}

// ProposerFor returns the node scheduled to propose (height, round).
func (c *Cluster) ProposerFor(height uint64, round uint32) *node.Node {
	v := c.Engine.ScheduledProposer(height, round)
	return c.NodeByAddr[v.Address]
}

// DrivePropose lets the scheduled proposer build the next block and then
// drains the network, returning the produced block.
func (c *Cluster) DrivePropose(round uint32) (*types.Block, error) {
	head := c.Nodes[0].Head()
	p := c.ProposerFor(head.Height+1, round)
	if round == 0 {
		return p.ProposeBlock()
	}
	return p.ProposeRound(round, nil)
}

// Drain delivers all currently queued network messages until no progress
// is made in a full round. It is fully deterministic and does not sleep.
func (c *Cluster) Drain() int { return c.Net.DeliverAll() }

// SignTx constructs and signs a transaction.
func SignTx(key ed25519.PrivateKey, to types.Address, nonce, amount, fee uint64) *types.Tx {
	tx := &types.Tx{
		FromPub: key.Public().(ed25519.PublicKey),
		To:      to,
		Nonce:   nonce,
		Amount:  amount,
		Fee:     fee,
	}
	tx.Sig = ed25519.Sign(key, tx.SigningBytes())
	return tx
}

// Addr returns the address for key index i.
func (c *Cluster) Addr(i int) types.Address {
	return types.AddressFromPub(c.Keys[i].Public().(ed25519.PublicKey))
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// seedReader is a deterministic zero-byte generator seeded per key; Go's
// ed25519 key generation only needs random-looking distinct streams, and
// using a counter-seeded SHAKE-like reader here keeps clusters stable.
type seedReader struct{ seed int64 }

func newSeedReader(seed int64) *seedReader { return &seedReader{seed: seed} }

func (r *seedReader) Read(p []byte) (int, error) {
	// Simple deterministic xorshift64* stream.
	state := uint64(r.seed)*6364136223846793005 + 1442695040888963407
	if state == 0 {
		state = 0x9E3779B97F4A7C15
	}
	for i := range p {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		p[i] = byte(state * 2685821657736338717 >> 56)
	}
	return len(p), nil
}
