// Package node wires chain state, mempool, consensus and the simulated network
// into a single local validator process.
package node

import (
	"fmt"
	"log"
	"os"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/pool"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// Node is one simulated validator.
type Node struct {
	Name string
	Addr types.Address

	net      *simnet.Network
	chain    *consensus.Chain
	set      *consensus.Set
	pool     *pool.Pool
	vals     []*types.Validator
	pending  []*types.Evidence // equivocation evidence waiting to be packed
	consumed map[string]bool   // evidence already included in any valid block
	proposed map[uint64]types.Hash
	logger   *log.Logger
}

// Config constructs a node.
type Config struct {
	Name         string
	Addr         types.Address
	Net          *simnet.Network
	Vals         []*types.Validator
	Genesis      *types.Block
	GenesisState *state.State
	Logger       *log.Logger
}

// New builds and registers a node with an independent copy of genesis state.
func New(cfg Config) *Node {
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stdout, "", log.Lmicroseconds)
	}
	c := consensus.NewChainWithSet(cfg.Vals, cfg.Genesis, cfg.GenesisState)
	n := &Node{
		Name:     cfg.Name,
		Addr:     cfg.Addr,
		net:      cfg.Net,
		chain:    c,
		set:      consensus.NewSet(cfg.Vals),
		pool:     pool.New(c.HeadState()),
		vals:     cfg.Vals,
		consumed: make(map[string]bool),
		proposed: make(map[uint64]types.Hash),
		logger:   cfg.Logger,
	}
	cfg.Net.Register(cfg.Name, n)
	return n
}

func (n *Node) infof(format string, args ...any) {
	n.logger.Printf("[%s] %s", n.Name, fmt.Sprintf(format, args...))
}

// Chain exposes the consensus chain (tests use it for assertions).
func (n *Node) Chain() *consensus.Chain { return n.chain }

// ProposerFor reports whether this node is the scheduled proposer at height h.
func (n *Node) ProposerFor(h uint64) bool { return n.set.ProposerAt(h) == n.Addr }

// Receive handles one inbound network message.
func (n *Node) Receive(m simnet.Message) {
	switch m.Kind {
	case simnet.MsgBlock:
		n.receiveBlock(m.From, m.Block)
	case simnet.MsgVote:
		n.receiveVote(m.From, m.Vote)
	case simnet.MsgTx:
		n.receiveTx(m.From, m.Tx)
	}
}

func (n *Node) receiveBlock(from string, b *types.Block) {
	h := types.HashBlock(b)
	// Detect double-propose gossip: same height, same proposer, two distinct
	// block hashes. Construct slashing evidence and queue it for the next block.
	if b.Proposer != (types.Address{}) {
		if first, ok := n.proposed[b.Height]; ok && first != h {
			n.infof("input: block h=%d from=%s hash=%x — DOUBLE-PROPOSE by %x (saw %x)",
				b.Height, from, h[:8], b.Proposer[:8], first[:8])
			if prior := n.chain.GetBlock(first); prior != nil {
				if ev, err := consensus.NewDoubleProposeEvidence(prior.Block, b); err == nil {
					n.queueEvidence(ev)
					n.infof("decision: double-propose evidence queued against %x at h=%d",
						b.Proposer[:8], b.Height)
				}
			}
		} else if !ok {
			n.proposed[b.Height] = h
		}
	}
	node2, err := n.chain.AddBlock(b)
	if err != nil {
		n.infof("input: block h=%d from=%s hash=%x -> REJECT (%v)", b.Height, from, h[:8], err)
		return
	}
	n.infof("input: block h=%d from=%s hash=%x proposer=%x txs=%d ev=%d -> ACCEPT (head h=%d root=%x finality h=%d)",
		b.Height, from, h[:8], b.Proposer[:8], len(b.Txs), len(b.Evidence),
		node2.Block.Height, node2.Block.StateRoot[:8], n.chain.Finalized().Block.Height)
	// Evidence included in any valid block is spent: mark it so competing
	// blocks never slash the same offence twice (deterministic state roots).
	for _, ev := range b.Evidence {
		n.consumed[evidenceKey(ev)] = true
	}
	if len(b.Evidence) > 0 {
		kept := n.pending[:0]
		for _, ev := range n.pending {
			if !n.consumed[evidenceKey(ev)] {
				kept = append(kept, ev)
			}
		}
		n.pending = kept
	}
	// Rebase the mempool onto the new head state, preserving pending txs.
	n.pool.Rebase(n.chain.HeadState())

	// Record block authors for double-propose detection on this node too.
	if b.Proposer != (types.Address{}) {
		n.proposed[b.Height] = h
	}
}

func (n *Node) receiveVote(from string, v *types.Vote) {
	ev, err := n.chain.AddVote(v)
	if ev != nil {
		n.infof("input: vote h=%d from=%s voter=%x block=%x -> EQUIVOCATION evidence collected (%v)",
			v.Height, from, v.Voter[:8], v.BlockHash[:8], err)
		n.pending = append(n.pending, ev)
		return
	}
	if err != nil {
		n.infof("input: vote h=%d voter=%x -> REJECT (%v)", v.Height, v.Voter[:8], err)
		return
	}
	n.infof("input: vote h=%d from=%s voter=%x block=%x -> ACCEPT (finality h=%d)",
		v.Height, from, v.Voter[:8], v.BlockHash[:8], n.chain.Finalized().Block.Height)
}

func (n *Node) receiveTx(from string, t *types.Tx) {
	if err := n.pool.Add(t); err != nil {
		n.infof("input: tx nonce=%d from=%s -> REJECT (%v)", t.Nonce, from, err)
		return
	}
	n.infof("input: tx nonce=%d from=%s fee=%d -> ACCEPT pool=%d", t.Nonce, from, t.Fee, n.pool.Size())
}

func evidenceKey(ev *types.Evidence) string {
	return fmt.Sprintf("%s:%d:%x", ev.Kind, ev.Height, ev.Offender)
}

// queueEvidence stores slashing evidence unless an identical offence was
// already packed into a valid block (at-most-once slashing keeps competing
// branches' state roots identical).
func (n *Node) queueEvidence(ev *types.Evidence) {
	k := evidenceKey(ev)
	if n.consumed[k] {
		return
	}
	for _, e := range n.pending {
		if evidenceKey(e) == k {
			return
		}
	}
	n.pending = append(n.pending, ev)
}

// ProduceBlock builds a block at head+1 if this node is the scheduled proposer
// and gossips it. Returns nil when not this node's turn.
//
// The block executes against a clone of the head state, so the state root is
// computed before gossip; every other node recomputes the same root.
func (n *Node) ProduceBlock() *types.Block {
	head := n.chain.Head()
	height := head.Block.Height + 1
	if !n.ProposerFor(height) {
		return nil
	}
	headState := n.chain.StateOf(head).Clone()

	// Apply gathered slashing evidence (1/3 of offender balance each),
	// skipping anything already included by a competing valid block.
	evs := make([]*types.Evidence, 0, len(n.pending))
	for _, ev := range n.pending {
		if !n.consumed[evidenceKey(ev)] {
			evs = append(evs, ev)
		}
	}
	n.pending = nil
	burn := uint64(0)
	for _, ev := range evs {
		bal := headState.GetBalance(ev.Offender)
		burn += headState.Slash(ev.Offender, bal/3)
	}

	// Pack mempool transactions deterministically (fee, nonce, arrival).
	txs := n.pool.Ordered(100)
	executed := make([]*types.Tx, 0, len(txs))
	for _, t := range txs {
		if err := headState.ApplyTx(t); err != nil {
			n.infof("produce: skip tx nonce=%d (%v)", t.Nonce, err)
			continue
		}
		burn += t.Fee
		executed = append(executed, t)
	}

	b := &types.Block{
		Height:       height,
		Proposer:     n.Addr,
		PrevHash:     head.Hash,
		StateRoot:    headState.Root(),
		Txs:          executed,
		Evidence:     evs,
		JustifyVotes: n.chain.Votes().VotesAt(head.Block.Height),
	}

	// Self-apply then gossip so this node also runs full validation.
	n.receiveBlock(n.Name, b)
	n.net.Gossip(simnet.Message{Kind: simnet.MsgBlock, From: n.Name, Block: b})
	n.infof("produce: h=%d parent=%x txs=%d ev=%d root=%x judg=vote-quorum(parent h=%d)",
		height, head.Hash[:8], len(executed), len(evs), b.StateRoot[:8], head.Block.Height)
	return b
}

// CastVote gossips this node's vote for its current canonical head, at most
// one vote per height. Returns nil if it already voted at that height.
func (n *Node) CastVote() *types.Vote {
	head := n.chain.Head()
	h := head.Block.Height
	if h == 0 {
		return nil // genesis is implicitly finalized
	}
	if n.chain.Votes().HasVoted(h, n.Addr) {
		return nil
	}
	v := types.SignVote(&types.Vote{Height: h, Voter: n.Addr, BlockHash: head.Hash})
	n.receiveVote(n.Name, v)
	n.net.Gossip(simnet.Message{Kind: simnet.MsgVote, From: n.Name, Vote: v})
	n.infof("vote: h=%d block=%x stake=%d", h, head.Hash[:8], n.chain.StateOf(head).GetBalance(n.Addr))
	return v
}

// SubmitTx validates against the local state, enters the mempool and gossips.
func (n *Node) SubmitTx(t *types.Tx) error {
	if err := n.pool.Add(t); err != nil {
		return err
	}
	n.receiveTx(n.Name, t)
	n.net.Gossip(simnet.Message{Kind: simnet.MsgTx, From: n.Name, Tx: t})
	return nil
}

// Head returns the current canonical head block.
func (n *Node) Head() *types.Block { return n.chain.Head().Block }

// FinalizedHeight returns the latest finalized height.
func (n *Node) FinalizedHeight() uint64 { return n.chain.Finalized().Block.Height }

// StateAt returns a copy of the state at the current canonical head.
func (n *Node) StateAt() *state.State { return n.chain.StateOf(n.chain.Head()).Clone() }
