package consensus

import (
	"bytes"

	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// BlockNode is a stored block with bookkeeping for fork choice.
type BlockNode struct {
	Block  *types.Block
	Hash   types.Hash
	Parent *BlockNode

	// state is the world state after executing this block; feeBurn is the
	// cumulative fees destroyed along this branch (slashed stake is NOT
	// destroyed — it moves to the protocol public account). Conservation:
	// total(balance incl. protocol account)+feeBurn == genesis total.
	state   *state.State
	feeBurn uint64

	justified bool // carries a valid 2/3+ quorum for itself
	finalized bool
}

// Chain stores all received blocks, votes and finalized state for one node.
type Chain struct {
	set       *Set
	genesis   *BlockNode
	nodes     map[types.Hash]*BlockNode
	children  map[types.Hash][]*BlockNode
	votes     *VotePool
	finalized *BlockNode
}

// NewChain creates a chain whose genesis block carries the initial state.
// The genesis block (height 0) is considered justified and finalized.
func NewChain(genesis *types.Block, genesisState *state.State) *Chain {
	gh := types.HashBlock(genesis)
	g := &BlockNode{
		Block: genesis, Hash: gh,
		state: genesisState.Clone(), justified: true, finalized: true,
	}
	return &Chain{
		set:       NewSet(nil),
		genesis:   g,
		nodes:     map[types.Hash]*BlockNode{g.Hash: g},
		children:  make(map[types.Hash][]*BlockNode),
		votes:     NewVotePool(),
		finalized: g,
	}
}

// Genesis returns the genesis node.
func (c *Chain) Genesis() *BlockNode { return c.genesis }

// Votes exposes the chain's vote pool (driving votes & finalization).
func (c *Chain) Votes() *VotePool { return c.votes }

// StateOf returns the post-state of a stored block.
func (c *Chain) StateOf(n *BlockNode) *state.State { return n.state }

// HeadState returns the post-state of the canonical head (a live pointer the
// mempool reads for admission; block execution always uses clones).
func (c *Chain) HeadState() *state.State { return c.Head().state }

// FeeBurnOn returns the cumulative burned fees along n's branch.
func (c *Chain) FeeBurnOn(n *BlockNode) uint64 { return n.feeBurn }

// BurnOn is kept for compatibility; slashes no longer burn, so this equals
// the cumulative burned fees.
func (c *Chain) BurnOn(n *BlockNode) uint64 { return n.feeBurn }

// AddVote forwards a vote to the pool and re-evaluates finalization.
// Equivocation evidence (if any) is returned for inclusion in a block.
func (c *Chain) AddVote(v *types.Vote) (*types.Evidence, error) {
	ev, err := c.votes.AddVote(v)
	if err != nil {
		return ev, err
	}
	c.refreshFinalization()
	return ev, nil
}

// AddBlock validates and stores a block.
//
// Validation order (first failure wins):
//  1. duplicate block -> ErrDuplicateBlock (re-broadcast is harmless)
//  2. parent must be known -> ErrUnknownParent
//  3. proposer must match the weighted schedule -> ErrWrongProposer
//  4. proposer signature must verify against the schedule's PUBLIC key
//     -> ErrBadProposerSig
//  5. evidence must be well-formed and signed by the offender's key
//     -> ErrEvidenceInvalid
//  6. parent justification must carry >=2/3 votes -> ErrJustification
//  7. execution state root must match -> ErrBadStateRoot
//  8. block must not conflict with finalized history -> ErrFinalizedReorg
func (c *Chain) AddBlock(b *types.Block) (*BlockNode, error) {
	h := types.HashBlock(b)
	if _, dup := c.nodes[h]; dup {
		return c.nodes[h], ErrDuplicateBlock
	}
	parent, ok := c.nodes[b.PrevHash]
	if !ok {
		return nil, ErrUnknownParent
	}
	if b.Height != parent.Block.Height+1 {
		return nil, ErrUnknownParent
	}
	// Proposer schedule is defined by the genesis set layout; stake itself
	// (voting weight) follows live state. The schedule ring is stable.
	want := c.set.ProposerAt(b.Height)
	if want != b.Proposer {
		return nil, ErrWrongProposer
	}
	// The proposal must be signed by the scheduled proposer's private key.
	// Every node checks it against the PUBLIC key in the validator set, so a
	// block claiming another identity (or carrying no/forged signature) is
	// rejected identically everywhere.
	if !types.VerifyBlock(b, c.set.PubKey(want)) {
		return nil, ErrBadProposerSig
	}
	for _, ev := range b.Evidence {
		if err := verifyEvidence(c.set, ev); err != nil {
			return nil, err
		}
	}
	// Justification is proven solely by votes embedded in the block.
	if b.Height > 1 {
		total := uint64(0)
		for _, a := range c.set.Addrs() {
			total += parent.state.GetBalance(a)
		}
		if c.JustificationPower(b, parent) < quorum(total) {
			return nil, ErrJustification
		}
		// The embedded quorum cryptographically proves parent justification;
		// this fact propagates with the block and needs no vote timing.
		parent.justified = true
	}

	// Deterministic execution: same block + same parent state on every node
	// must produce the same resulting state root.
	st := parent.state.Clone()
	feeBurn := parent.feeBurn
	// Slashed stake moves to the network-wide protocol public account; it is
	// part of the committed state, not of the burn counter.
	for _, ev := range b.Evidence {
		bal := st.GetBalance(ev.Offender)
		st.Slash(ev.Offender, bal/3)
	}
	for _, t := range b.Txs {
		if err := st.ApplyTx(t); err != nil {
			return nil, err
		}
		feeBurn += t.Fee
	}
	root := st.Root()
	if !bytes.Equal(root[:], b.StateRoot[:]) {
		return nil, ErrBadStateRoot
	}

	n := &BlockNode{
		Block: b, Hash: h, Parent: parent,
		state: st, feeBurn: feeBurn,
	}
	c.nodes[h] = n
	c.children[parent.Hash] = append(c.children[parent.Hash], n)

	// Finality-protected history cannot be abandoned: a new block must sit on
	// a chain that includes the current finalized block.
	if !c.extendsFinalized(n) {
		delete(c.nodes, h)
		c.children[parent.Hash] = c.children[parent.Hash][:len(c.children[parent.Hash])-1]
		return nil, ErrFinalizedReorg
	}

	n.justified = c.justified(h, b.Height)
	c.refreshFinalization()
	return n, nil
}

func (c *Chain) extendsFinalized(n *BlockNode) bool {
	fh := c.finalized.Hash
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Hash == fh {
			return true
		}
		if cur.Block.Height < c.finalized.Block.Height {
			return false
		}
	}
	return false
}

// Head returns the canonical head chosen by the deterministic fork-choice rule.
func (c *Chain) Head() *BlockNode { return c.chooseHead() }

// Finalized returns the latest finalized block node.
func (c *Chain) Finalized() *BlockNode { return c.finalized }

// GetBlock fetches a stored block by hash (any fork).
func (c *Chain) GetBlock(h types.Hash) *BlockNode { return c.nodes[h] }

// childrenOf returns stored children of a block.
func (c *Chain) childrenOf(h types.Hash) []*BlockNode { return c.children[h] }
