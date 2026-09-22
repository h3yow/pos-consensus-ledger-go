// Package node wires consensus rules, block DAG storage, the mempool and
// the simulated network into one local validator process. Nodes gossip
// transactions/blocks/votes/evidence, track the canonical head with the
// deterministic fork-choice rule, enforce finality and slash observed
// equivocation. All public entry points are goroutine-safe.
package node

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/internal/mempool"
	"github.com/h3yow/pos-consensus-ledger-go/internal/network"
	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/store"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Logger is the minimal logging sink used for inputs and decisions.
type Logger interface {
	Printf(format string, args ...any)
}

// Sentinel errors for proposal/voting attempts.
var (
	ErrNotYourTurn      = errors.New("node: not the scheduled proposer for this slot")
	ErrAlreadyVotedSlot = errors.New("node: already voted in this height/round")
	ErrNoHead           = errors.New("node: canonical head not ready")
	ErrBlockNotInStore  = errors.New("node: voted block hash unknown")
)

// maxTxsPerBlock bounds packing but keeps evaluation fully deterministic.
const maxTxsPerBlock = 256

// Node is a simulated validator node.
type Node struct {
	mu sync.Mutex

	id     string
	pub    ed25519.PublicKey
	key    ed25519.PrivateKey
	eng    *consensus.Engine
	net    *network.SimNetwork
	store  *store.Store
	votes  *store.VotesStore
	tally  *consensus.Tally
	pool   *mempool.Mempool
	logger Logger

	headHash    types.Hash
	finalHash   types.Hash
	finalHeight uint64

	// pendingEv are gossiped evidence not yet included in a canonical block.
	pendingEv map[types.Hash]*types.Evidence

	// orphans buffers blocks whose parent is not yet known.
	orphans []*types.Block
}

// Config constructs a node.
type Config struct {
	ID           string
	Key          ed25519.PrivateKey
	Engine       *consensus.Engine
	GenesisBlock *types.Block
	Net          *network.SimNetwork
	Logger       Logger
	PoolCap      int
}

// New creates and registers a node, anchoring the shared genesis block.
func New(cfg Config) (*Node, error) {
	if cfg.GenesisBlock == nil || cfg.GenesisBlock.Height != 0 {
		return nil, errors.New("node: genesis block required at height 0")
	}
	genRoot := state.FromGenesis(cfg.Engine.Genesis()).Root()
	if cfg.GenesisBlock.StateRoot != genRoot {
		return nil, fmt.Errorf("node: genesis state root mismatch %s != %s",
			cfg.GenesisBlock.StateRoot, genRoot)
	}
	if !cfg.GenesisBlock.VerifySig() {
		return nil, errors.New("node: genesis block signature invalid")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(log.Writer(), "["+cfg.ID+"] ", log.LstdFlags|log.Lmicroseconds)
	}
	poolCap := cfg.PoolCap
	if poolCap <= 0 {
		poolCap = 4096
	}
	genState := state.FromGenesis(cfg.Engine.Genesis())
	st := store.New()
	st.PutBlock(cfg.GenesisBlock)
	st.PutState(cfg.GenesisBlock.Hash(), genState)

	n := &Node{
		id:        cfg.ID,
		key:       cfg.Key,
		pub:       cfg.Key.Public().(ed25519.PublicKey),
		eng:       cfg.Engine,
		net:       cfg.Net,
		store:     st,
		votes:     store.NewVotes(),
		tally:     consensus.NewTally(),
		pool:      mempool.New(genState, poolCap),
		logger:    logger,
		headHash:  cfg.GenesisBlock.Hash(),
		finalHash: cfg.GenesisBlock.Hash(),
		pendingEv: map[types.Hash]*types.Evidence{},
	}
	if cfg.Net != nil {
		cfg.Net.Register(n)
	}
	n.logger.Printf("init id=%s addr=%s genesis=%s totalWeight=%d threshold=%d",
		n.id, types.AddressFromPub(n.pub), n.headHash,
		cfg.Engine.Validators().TotalWeight(), cfg.Engine.Threshold())
	return n, nil
}

// ID implements network.Handler.
func (n *Node) ID() string { return n.id }

// sign produces an ed25519 signature with this node's key.
func (n *Node) sign(msg []byte) []byte { return ed25519.Sign(n.key, msg) }

// HandleMessage implements network.Handler and dispatches by kind.
func (n *Node) HandleMessage(m types.Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch m.Kind {
	case types.MsgTx:
		if err := n.handleTxLocked(m.Tx, m.From); err != nil {
			n.logger.Printf("recv tx from=%s rejected: %v", m.From, err)
		}
	case types.MsgBlock:
		if err := n.handleBlockLocked(m.Block, m.From); err != nil {
			n.logger.Printf("recv block from=%s h=%d r=%d rejected: %v",
				m.From, m.Block.Height, m.Block.Round, err)
		}
	case types.MsgVote:
		if err := n.handleVoteLocked(m.Vote, m.From); err != nil {
			n.logger.Printf("recv vote from=%s h=%d r=%d rejected: %v",
				m.From, m.Vote.Height, m.Vote.Round, err)
		}
	case types.MsgEvidence:
		n.handleEvidenceLocked(m.Evidence, m.From)
	default:
		n.logger.Printf("recv unknown message kind=%d from=%s", m.Kind, m.From)
	}
}

// SubmitTx adds a locally created transaction and gossips it.
func (n *Node) SubmitTx(tx *types.Tx) error {
	n.mu.Lock()
	err := n.pool.Add(tx)
	n.mu.Unlock()
	if err != nil {
		n.logger.Printf("submit tx=%s rejected: %v", tx.Hash(), err)
		return err
	}
	n.logger.Printf("submit tx=%s from=%s nonce=%d amount=%d fee=%d",
		tx.Hash(), tx.Sender(), tx.Nonce, tx.Amount, tx.Fee)
	if n.net != nil {
		n.net.Broadcast(types.Message{Kind: types.MsgTx, From: n.id, Tx: tx})
	}
	return nil
}

// handleTx admits a remote transaction if valid and new.
func (n *Node) handleTxLocked(tx *types.Tx, src string) error {
	if tx == nil {
		return errors.New("nil tx")
	}
	err := n.pool.Add(tx)
	if err != nil {
		return err
	}
	n.logger.Printf("accept tx=%s from=%s nonce=%d fee=%d",
		tx.Hash(), src, tx.Nonce, tx.Fee)
	return nil
}

// handleBlock validates and stores a block, gossips it on first sight,
// retries buffered orphans, re-runs fork choice and votes if scheduled.
func (n *Node) handleBlockLocked(b *types.Block, src string) error {
	if b == nil {
		return errors.New("nil block")
	}
	h := b.Hash()
	if n.store.Has(h) {
		n.logger.Printf("block dup h=%d r=%d hash=%s from=%s -> ignore",
			b.Height, b.Round, h, src)
		return nil
	}
	// Finality guard: a block at/below finalized height that is not the
	// finalized block itself can never be accepted.
	if b.Height <= n.finalHeight && h != n.finalHash {
		n.logger.Printf("block REJECT finality h=%d finalizedH=%d hash=%s",
			b.Height, n.finalHeight, h)
		return consensus.ErrFinalizedRollback
	}

	parent, perr := n.store.GetBlock(b.ParentHash)
	if perr != nil {
		n.orphans = append(n.orphans, b)
		n.logger.Printf("block orphan h=%d r=%d hash=%s parent=%s buffered",
			b.Height, b.Round, h, b.ParentHash)
		return nil
	}
	parentState, serr := n.store.GetState(parent.Hash())
	if serr != nil {
		return serr
	}
	st, err := n.eng.ValidateBlock(b, parent, parentState)
	if err != nil {
		return err
	}
	n.store.PutBlock(b)
	n.store.PutState(h, st)
	n.logger.Printf("block ACCEPT h=%d r=%d hash=%s parent=%s proposer=%s txs=%d ev=%d stateRoot=%s from=%s",
		b.Height, b.Round, h, b.ParentHash, b.Proposer, len(b.Txs), len(b.Evidence), b.StateRoot, src)

	// First sight: relay exactly once. Duplicate broadcasts are absorbed
	// by the Has(h) check above on peers.
	if n.net != nil && src != n.id {
		n.net.Broadcast(types.Message{Kind: types.MsgBlock, From: n.id, Block: b})
	}

	// Proposal equivocation: another block from the same proposer/slot?
	for _, other := range n.store.BlocksAt(b.Height) {
		if ev := n.eng.DetectBlockEquivocation(b, other); ev != nil {
			n.relayEvidence(ev, "block-dual-proposal")
		}
	}

	n.tryOrphans()
	if err := n.applyForkChoice(); err != nil {
		return err
	}
	n.autoMaybeVote()
	n.checkFinality()
	return nil
}

// tryOrphans re-attempts buffered blocks whose parents may have arrived.
func (n *Node) tryOrphans() {
	if len(n.orphans) == 0 {
		return
	}
	remaining := make([]*types.Block, 0, len(n.orphans))
	for _, b := range n.orphans {
		if n.store.Has(b.Hash()) {
			continue
		}
		if n.store.Has(b.ParentHash) {
			if err := n.handleBlockLocked(b, n.id+"-orphan"); err != nil {
				n.logger.Printf("orphan retry failed h=%d: %v", b.Height, err)
			}
			continue
		}
		remaining = append(remaining, b)
	}
	n.orphans = remaining
}

// handleVote records a vote, detects equivocation, updates the tally and
// checks finality.
func (n *Node) handleVoteLocked(v *types.Vote, src string) error {
	if err := n.eng.ValidateVote(v); err != nil {
		return err
	}
	// We only tally votes for blocks we know (prevents unanchored votes).
	if !n.store.Has(v.BlockHash) {
		return ErrBlockNotInStore
	}
	vh := v.Hash()
	prior := n.votes.Add(v)
	if prior != nil {
		if prior.Hash() == vh {
			return nil // identical rebroadcast: idempotent
		}
		if ev := n.eng.DetectVoteEquivocation(prior, v); ev != nil {
			n.relayEvidence(ev, "vote-dual-sign")
		}
		return nil // conflicting vote not added to the effective tally
	}
	n.tally.Add(v)
	n.logger.Printf("vote ACCEPT h=%d r=%d block=%s voter=%s weight=%d tally=%d/%d from=%s",
		v.Height, v.Round, v.BlockHash, v.Voter,
		n.weightOf(v.Voter),
		n.tally.WeightFor(consensus.Slot{Height: v.Height, Round: v.Round}, v.BlockHash, n.eng.Validators()),
		n.eng.Threshold(), src)
	if n.net != nil && src != n.id {
		n.net.Broadcast(types.Message{Kind: types.MsgVote, From: n.id, Vote: v})
	}
	n.checkFinality()
	return nil
}

func (n *Node) weightOf(addr types.Address) uint64 {
	if val, ok := n.eng.Validators().Get(addr); ok {
		return val.Weight
	}
	return 0
}

// handleEvidence buffers valid new evidence and relays it once.
func (n *Node) handleEvidenceLocked(ev *types.Evidence, src string) {
	if ev == nil || !ev.Validator() {
		n.logger.Printf("evidence INVALID from=%s", src)
		return
	}
	eh := ev.Hash()
	if _, ok := n.pendingEv[eh]; ok {
		return
	}
	if _, isVal := n.eng.Validators().Get(ev.Offender()); !isVal {
		n.logger.Printf("evidence offender not validator: %s", ev.Offender())
		return
	}
	n.pendingEv[eh] = ev
	n.logger.Printf("evidence ACCEPT kind=%d h=%d r=%d offender=%s hash=%s from=%s",
		ev.Kind, ev.Height, ev.Round, ev.Offender(), eh, src)
	if n.net != nil && src != n.id {
		n.net.Broadcast(types.Message{Kind: types.MsgEvidence, From: n.id, Evidence: ev})
	}
}

// relayEvidence buffers and gossips locally-detected evidence.
func (n *Node) relayEvidence(ev *types.Evidence, reason string) {
	if ev == nil {
		return
	}
	eh := ev.Hash()
	if _, ok := n.pendingEv[eh]; ok {
		return
	}
	n.pendingEv[eh] = ev
	n.logger.Printf("evidence DETECT %s kind=%d h=%d r=%d offender=%s hash=%s",
		reason, ev.Kind, ev.Height, ev.Round, ev.Offender(), eh)
	if n.net != nil {
		n.net.Broadcast(types.Message{Kind: types.MsgEvidence, From: n.id, Evidence: ev})
	}
}

// applyForkChoice recomputes the canonical head over all stored tips.
func (n *Node) applyForkChoice() error {
	tips := n.store.Tips()
	cands := make([]types.Hash, 0, len(tips))
	for _, b := range tips {
		cands = append(cands, b.Hash())
	}
	best, err := n.eng.ForkChoice(n.store, cands, n.finalHash, n.finalHeight)
	if err != nil {
		return err
	}
	if best != n.headHash {
		old := n.headHash
		n.headHash = best
		hb, _ := n.store.GetBlock(best)
		hs, _ := n.store.GetState(best)
		n.pool.SetBase(hs)
		n.logger.Printf("fork-choice head SWITCH %s -> %s (h=%d r=%d)",
			old, best, hb.Height, hb.Round)
	} else {
		hb, _ := n.store.GetBlock(best)
		hs, _ := n.store.GetState(best)
		n.pool.SetBase(hs)
		n.logger.Printf("fork-choice head KEEP %s h=%d r=%d", best, hb.Height, hb.Round)
	}
	return nil
}

// autoMaybeVote casts this validator's honest vote for the canonical head
// of the current proposal height if it has not voted in that slot yet.
func (n *Node) autoMaybeVote() {
	head := n.Head()
	sl := consensus.Slot{Height: head.Height, Round: head.Round}
	if n.tally.HasVoted(sl, types.AddressFromPub(n.pub)) ||
		n.votes.Get(types.AddressFromPub(n.pub), head.Height, head.Round) != nil {
		return
	}
	if _, err := n.VoteFor(head.Height, head.Round, head.Hash()); err != nil {
		n.logger.Printf("auto-vote failed h=%d r=%d: %v", head.Height, head.Round, err)
	}
}

// checkFinality looks for a threshold-carrying block at the head slot and
// anchors it (with its ancestors) as finalized.
func (n *Node) checkFinality() {
	head := n.Head()
	for h := head.Height; h > n.finalHeight; h-- {
		chain, err := n.store.Chain(head.Hash())
		if err != nil {
			return
		}
		if h >= uint64(len(chain)) {
			continue
		}
		blk := chain[h]
		sl := consensus.Slot{Height: blk.Height, Round: blk.Round}
		finalHash := n.tally.FinalizedAt(sl, n.eng.Validators(), n.eng.Threshold())
		if finalHash != blk.Hash() {
			continue
		}
		n.finalHash = blk.Hash()
		n.finalHeight = blk.Height
		n.logger.Printf("FINALITY h=%d r=%d block=%s weight=%d/%d (ancestors through genesis anchored)",
			blk.Height, blk.Round, blk.Hash(),
			n.tally.WeightFor(sl, blk.Hash(), n.eng.Validators()),
			n.eng.Threshold())
		return
	}
}

// ProposeBlock honestly proposes round 0 for head+1 if this node is the
// scheduled proposer. Tests may use ProposeRound for higher-round/faulty
// scenarios.
func (n *Node) ProposeBlock() (*types.Block, error) {
	return n.ProposeRound(0, nil)
}

// ProposeRound builds and gossips a block at head+1 with the given round.
// extraEv is appended to locally pending evidence (used by adversarial
// test harnesses to force inclusion).
func (n *Node) ProposeRound(round uint32, extraEv []*types.Evidence) (*types.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	head, err := n.store.GetBlock(n.headHash)
	if err != nil {
		return nil, ErrNoHead
	}
	height := head.Height + 1
	scheduled := n.eng.ScheduledProposer(height, round)
	if scheduled.Address != types.AddressFromPub(n.pub) {
		return nil, fmt.Errorf("%w: h=%d r=%d scheduled=%s self=%s",
			ErrNotYourTurn, height, round, scheduled.Address, types.AddressFromPub(n.pub))
	}
	if len(n.store.BlocksAt(height)) > 0 {
		return nil, fmt.Errorf("node: already have proposal(s) at height %d", height)
	}

	txs := n.pool.Pack(maxTxsPerBlock)
	evs := n.collectEvidence(extraEv)

	st, err := n.store.GetState(head.Hash())
	if err != nil {
		return nil, err
	}
	next := n.buildBlockLocked(head, st, height, round, txs, evs)

	// Process locally first (validate, store, fork-choice, self-vote)...
	n.mu.Unlock()
	n.HandleMessage(types.Message{Kind: types.MsgBlock, From: n.id, Block: next})
	// ...then gossip to peers (self messages are not relayed inside the
	// handler, so broadcast exactly once here).
	if n.net != nil {
		n.net.Broadcast(types.Message{Kind: types.MsgBlock, From: n.id, Block: next})
	}
	n.mu.Lock()
	n.logger.Printf("PROPOSE h=%d r=%d hash=%s txs=%d ev=%d stateRoot=%s",
		next.Height, next.Round, next.Hash(), len(next.Txs), len(next.Evidence), next.StateRoot)
	return next, nil
}

// collectEvidence returns valid, not-yet-applied pending evidence sorted
// by hash, then removes the included ones from the pending set.
func (n *Node) collectEvidence(extra []*types.Evidence) []*types.Evidence {
	for _, ev := range extra {
		if ev != nil && ev.Validator() {
			n.pendingEv[ev.Hash()] = ev
		}
	}
	head, _ := n.store.GetBlock(n.headHash)
	st, _ := n.store.GetState(n.headHash)
	var out []*types.Evidence
	for eh, ev := range n.pendingEv {
		if ev.Height > head.Height {
			continue
		}
		if st.EvidenceApplied(eh) {
			delete(n.pendingEv, eh)
			continue
		}
		if _, ok := n.eng.Validators().Get(ev.Offender()); !ok {
			delete(n.pendingEv, eh)
			continue
		}
		out = append(out, ev)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Hash().Cmp(out[j].Hash()) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	for _, ev := range out {
		delete(n.pendingEv, ev.Hash())
	}
	return out
}

// buildBlockLocked constructs the signed block by simulating execution
// against a clone of the head state; the resulting state root is embedded.
func (n *Node) buildBlockLocked(head *types.Block, base *state.State, height uint64, round uint32, txs []*types.Tx, evs []*types.Evidence) *types.Block {
	b := &types.Block{
		Height:      height,
		Round:       round,
		ParentHash:  head.Hash(),
		Proposer:    types.AddressFromPub(n.pub),
		ProposerPub: append(ed25519.PublicKey(nil), n.pub...),
		Txs:         txs,
		Evidence:    evs,
		Timestamp:   0,
	}
	b.TxRoot = consensus.TxRoot(txs)

	st := base.Clone()
	// Re-apply in the same order consensus will verify; the proposer is
	// the fee sink. Pack() already validated, so this cannot fail for
	// honest proposals; if it does the tx set is simply excluded.
	kept := txs[:0]
	for _, tx := range txs {
		if err := st.Apply(tx, b.Proposer); err != nil {
			n.logger.Printf("proposal drops tx=%s: %v", tx.Hash(), err)
			continue
		}
		kept = append(kept, tx)
	}
	b.Txs = kept
	b.TxRoot = consensus.TxRoot(kept)

	g := n.eng.Genesis()
	for _, ev := range evs {
		st.SlashEvidence(ev, g.Treasury, g.SlashRateNumerator, g.SlashRateDenominator)
	}
	b.StateRoot = st.Root()
	b.Sig = n.sign(b.HeaderBytes())
	return b
}

// VoteFor signs and broadcasts a vote, refusing two different votes in
// the same (height, round) slot (that would be self-equivocation).
func (n *Node) VoteFor(height uint64, round uint32, h types.Hash) (*types.Vote, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	me := types.AddressFromPub(n.pub)
	if prior := n.votes.Get(me, height, round); prior != nil {
		if prior.BlockHash == h {
			return prior, nil
		}
		return nil, fmt.Errorf("%w: h=%d r=%d voted=%s new=%s",
			ErrAlreadyVotedSlot, height, round, prior.BlockHash, h)
	}
	if !n.store.Has(h) {
		return nil, fmt.Errorf("%w: %s", ErrBlockNotInStore, h)
	}
	blk, err := n.store.GetBlock(h)
	if err != nil || blk.Height != height || blk.Round != round {
		return nil, fmt.Errorf("vote target slot mismatch h=%d r=%d block=%+v", height, round, blk)
	}
	v := &types.Vote{
		Height:    height,
		Round:     round,
		BlockHash: h,
		Voter:     me,
		VoterPub:  append(ed25519.PublicKey(nil), n.pub...),
	}
	v.Sig = n.sign(v.SigningBytes())

	// Record locally first (no evidence against self).
	n.votes.Add(v)
	n.tally.Add(v)
	n.logger.Printf("VOTE cast h=%d r=%d block=%s voter=self weight=%d tally=%d/%d",
		height, round, h, n.weightOf(me),
		n.tally.WeightFor(consensus.Slot{Height: height, Round: round}, h, n.eng.Validators()),
		n.eng.Threshold())
	if n.net != nil {
		n.net.Broadcast(types.Message{Kind: types.MsgVote, From: n.id, Vote: v})
	}
	n.checkFinality()
	return v, nil
}

// ForceVote signs a vote with a node's key WITHOUT the honest slot guard,
// allowing test harnesses to simulate equivocating/double-signing nodes.
func (n *Node) ForceVote(height uint64, round uint32, h types.Hash) *types.Vote {
	n.mu.Lock()
	defer n.mu.Unlock()
	v := &types.Vote{
		Height:    height,
		Round:     round,
		BlockHash: h,
		Voter:     types.AddressFromPub(n.pub),
		VoterPub:  append(ed25519.PublicKey(nil), n.pub...),
	}
	v.Sig = n.sign(v.SigningBytes())
	n.logger.Printf("FORCE-VOTE (byzantine) h=%d r=%d block=%s", height, round, h)
	if n.net != nil {
		n.net.Broadcast(types.Message{Kind: types.MsgVote, From: n.id, Vote: v})
	}
	return v
}

// Head returns the current canonical block.
func (n *Node) Head() *types.Block {
	b, _ := n.store.GetBlock(n.headHash)
	return b
}

// HeadState returns the state after the canonical head.
func (n *Node) HeadState() *state.State {
	st, _ := n.store.GetState(n.headHash)
	return st
}

// Finalized returns (hash, height) of the latest finalized block.
func (n *Node) Finalized() (types.Hash, uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.finalHash, n.finalHeight
}

// Store exposes storage for test assertions.
func (n *Node) Store() *store.Store { return n.store }

// Tally exposes the vote tally for test assertions.
func (n *Node) Tally() *consensus.Tally { return n.tally }

// Pool exposes the mempool for test assertions.
func (n *Node) Pool() *mempool.Mempool { return n.pool }

// PendingEvidence exposes locally buffered evidence for assertions.
func (n *Node) PendingEvidence() map[types.Hash]*types.Evidence {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[types.Hash]*types.Evidence, len(n.pendingEv))
	for h, ev := range n.pendingEv {
		out[h] = ev
	}
	return out
}

// StdLogger builds a standard-library logger with a node prefix.
func StdLogger(prefix string) Logger {
	return log.New(log.Writer(), prefix, log.LstdFlags|log.Lmicroseconds)
}
