// Package consensus implements the deterministic PoS rule engine:
// genesis creation, block/vote validation, equivocation detection,
// weighted finality tally and the canonical fork-choice rule.
//
// It contains no networking or goroutine state; node.Node drives it.
package consensus

import (
	"errors"
	"fmt"

	"github.com/h3yow/pos-consensus-ledger-go/internal/state"
	"github.com/h3yow/pos-consensus-ledger-go/internal/store"
	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Validation errors give distinguishable rejection reasons in logs.
var (
	ErrMalformed         = errors.New("consensus: malformed block")
	ErrUnknownParent     = errors.New("consensus: unknown parent")
	ErrBadProposer       = errors.New("consensus: proposer not in validator set")
	ErrBadBlockSig       = errors.New("consensus: invalid block signature")
	ErrNotScheduled      = errors.New("consensus: proposer/round mismatch with schedule")
	ErrFinalizedRollback = errors.New("consensus: block conflicts with finalized chain")
	ErrBadStateRoot      = errors.New("consensus: state root mismatch")
	ErrBadTxRoot         = errors.New("consensus: transaction root mismatch")
	ErrInvalidEvidence   = errors.New("consensus: invalid evidence")
	ErrBadVoter          = errors.New("consensus: voter not in validator set")
	ErrBadVoteSig        = errors.New("consensus: invalid vote signature")
	ErrBadVoteSlot       = errors.New("consensus: vote height/round invalid")
	ErrDuplicateTx       = errors.New("consensus: duplicate transaction in block")
	ErrInvalidTx         = errors.New("consensus: block contains invalid transaction")
)

// Engine bundles genesis parameters and pure decision rules.
type Engine struct {
	genesis *types.Genesis
	vset    *ValidatorSet
}

// NewEngine builds an engine and validates genesis parameters.
func NewEngine(g *types.Genesis) (*Engine, error) {
	if g == nil {
		return nil, errors.New("consensus: nil genesis")
	}
	if len(g.Validators) == 0 {
		return nil, errors.New("consensus: no validators in genesis")
	}
	if g.SlashRateDenominator == 0 || g.SlashRateNumerator > g.SlashRateDenominator {
		return nil, errors.New("consensus: invalid slash rate")
	}
	if g.ThresholdDenominator == 0 || g.ThresholdNumerator == 0 ||
		g.ThresholdNumerator > g.ThresholdDenominator {
		return nil, errors.New("consensus: invalid finality threshold")
	}
	for _, v := range g.Validators {
		if v.Weight == 0 {
			return nil, errors.New("consensus: validator with zero weight")
		}
		if types.AddressFromPub(v.PubKey) != v.Address {
			return nil, fmt.Errorf("consensus: validator address does not match public key: %s", v.Address)
		}
	}
	return &Engine{genesis: g, vset: NewValidatorSet(g.Validators)}, nil
}

// Genesis returns the genesis configuration.
func (e *Engine) Genesis() *types.Genesis { return e.genesis }

// Validators returns the fixed validator set.
func (e *Engine) Validators() *ValidatorSet { return e.vset }

// Threshold is the stake tally strictly required for finality:
// totalWeight * numerator / denominator, and the vote must be STRICTLY
// greater, i.e. requiredWeight = floor(total*n/d) + 1.
func (e *Engine) Threshold() uint64 {
	total := e.vset.TotalWeight()
	g := e.genesis
	return total*g.ThresholdNumerator/g.ThresholdDenominator + 1
}

// ScheduledProposer returns the round-robin proposer for (height, round):
// index = (height-1+round) mod n over validators sorted by address.
func (e *Engine) ScheduledProposer(height uint64, round uint32) *types.Validator {
	if height == 0 {
		return e.vset.At(0)
	}
	idx := (int(height) - 1 + int(round)) % e.vset.Len()
	return e.vset.At(idx)
}

// MakeGenesisBlock builds the signed genesis block at height 0 carrying
// the genesis state root. The first sorted validator signs it.
func (e *Engine) MakeGenesisBlock(sign func(pub []byte, msg []byte) []byte) *types.Block {
	st := state.FromGenesis(e.genesis)
	gv := e.vset.At(0)
	b := &types.Block{
		Height:      0,
		Round:       0,
		ParentHash:  types.ZeroHash,
		Proposer:    gv.Address,
		ProposerPub: append([]byte(nil), gv.PubKey...),
		StateRoot:   st.Root(),
		Timestamp:   0,
	}
	b.Sig = sign(gv.PubKey, b.HeaderBytes())
	return b
}

// ValidateBlock verifies a block structurally and against the parent
// state, applies transactions and evidence deterministically, and returns
// the resulting state. Every node calling this on the same inputs gets the
// same state (and hence the same state root).
func (e *Engine) ValidateBlock(b, parent *types.Block, parentState *state.State) (*state.State, error) {
	if b == nil || parent == nil || parentState == nil {
		return nil, ErrMalformed
	}
	if b.Height != parent.Height+1 {
		return nil, fmt.Errorf("%w: height %d must follow parent height %d",
			ErrMalformed, b.Height, parent.Height)
	}
	if b.ParentHash != parent.Hash() {
		return nil, fmt.Errorf("%w: parent hash mismatch", ErrMalformed)
	}
	prop, ok := e.vset.Get(b.Proposer)
	if !ok {
		return nil, ErrBadProposer
	}
	if !b.VerifySig() {
		return nil, ErrBadBlockSig
	}
	scheduled := e.ScheduledProposer(b.Height, b.Round)
	if scheduled.Address != b.Proposer {
		return nil, fmt.Errorf("%w: height=%d round=%d scheduled=%s got=%s",
			ErrNotScheduled, b.Height, b.Round, scheduled.Address, b.Proposer)
	}

	// Transaction root must commit the exact tx list.
	if got := TxRoot(b.Txs); got != b.TxRoot {
		return nil, fmt.Errorf("%w: computed %s header %s", ErrBadTxRoot, got, b.TxRoot)
	}

	st := parentState.Clone()

	// Dedup transactions within the block.
	seenTx := map[types.Hash]bool{}
	for _, tx := range b.Txs {
		h := tx.Hash()
		if seenTx[h] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateTx, h)
		}
		seenTx[h] = true
		if err := st.Apply(tx, b.Proposer); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidTx, h, err)
		}
	}

	// Apply evidence in evidence-hash order for determinism. The state
	// remembers applied evidence hashes, so the same proof in two blocks
	// (or on two branches) slashes at most once.
	evs := make([]*types.Evidence, len(b.Evidence))
	copy(evs, b.Evidence)
	sortEvidence(evs)
	for _, ev := range evs {
		eh := ev.Hash()
		if st.EvidenceApplied(eh) {
			return nil, fmt.Errorf("%w: duplicate evidence %s", ErrInvalidEvidence, eh)
		}
		if !ev.Validator() {
			return nil, fmt.Errorf("%w: %s", ErrInvalidEvidence, eh)
		}
		// Evidence must target a strictly prior height.
		if ev.Height >= b.Height {
			return nil, fmt.Errorf("%w: evidence height %d >= block height %d",
				ErrInvalidEvidence, ev.Height, b.Height)
		}
		if _, ok := e.vset.Get(ev.Offender()); !ok {
			return nil, fmt.Errorf("%w: offender %s not a validator",
				ErrInvalidEvidence, ev.Offender())
		}
		g := e.genesis
		st.SlashEvidence(ev, g.Treasury, g.SlashRateNumerator, g.SlashRateDenominator)
	}

	if got := st.Root(); got != b.StateRoot {
		return nil, fmt.Errorf("%w: computed %s header %s", ErrBadStateRoot, got, b.StateRoot)
	}
	_ = prop
	return st, nil
}

// ValidateVote checks a vote structurally.
func (e *Engine) ValidateVote(v *types.Vote) error {
	if v == nil || v.Height == 0 {
		return ErrBadVoteSlot
	}
	if _, ok := e.vset.Get(v.Voter); !ok {
		return ErrBadVoter
	}
	if !v.VerifySig() {
		return ErrBadVoteSig
	}
	return nil
}

// ForkChoice selects the canonical head among candidate block hashes.
//
// Rule (deterministic, honest-majority convergent):
//  1. Every candidate chain must extend the finalized tip; otherwise
//     ErrFinalizedRollback (finalized blocks can never be rolled back).
//  2. Compare two chains slot by slot starting at the first height where
//     they differ; at that slot prefer (lower round, then lower hash).
//  3. Equal prefixes: the longer chain wins; identical chains compare equal.
func (e *Engine) ForkChoice(st *store.Store, candidates []types.Hash, finalizedHash types.Hash, finalizedHeight uint64) (types.Hash, error) {
	// Candidates that do not extend the finalized tip are invalid after
	// finality: EXCLUDE them (e.g. an old competing block left as a tip),
	// rather than aborting selection for every other valid chain.
	chains := make([][]*types.Block, 0, len(candidates))
	for _, h := range candidates {
		ch, err := st.Chain(h)
		if err != nil {
			return types.ZeroHash, err
		}
		// ch is ordered by height starting at 0, so block height == index.
		if uint64(len(ch)) <= finalizedHeight {
			continue
		}
		if ch[finalizedHeight].Hash() != finalizedHash {
			continue
		}
		chains = append(chains, ch)
	}

	best := -1
	for i, ch := range chains {
		if best == -1 || compareChains(ch, chains[best]) > 0 {
			best = i
		}
	}
	if best == -1 {
		return types.ZeroHash, errors.New("consensus: no fork-choice candidates")
	}
	return chains[best][len(chains[best])-1].Hash(), nil
}

// compareChains returns >0 if a should be canonical over b.
func compareChains(a, b []*types.Block) int {
	common := len(a)
	if len(b) < common {
		common = len(b)
	}
	// Find first differing slot (they share genesis by construction).
	for i := 0; i < common; i++ {
		ha, hb := a[i].Hash(), b[i].Hash()
		if ha == hb {
			continue
		}
		if a[i].Round != b[i].Round {
			if a[i].Round < b[i].Round {
				return 1
			}
			return -1
		}
		if c := hb.Cmp(ha); c != 0 {
			return c // lower hash wins -> invert comparison
		}
	}
	// Prefixes equal up to the shorter chain: longer wins.
	switch {
	case len(a) > len(b):
		return 1
	case len(a) < len(b):
		return -1
	default:
		return 0
	}
}

// DetectBlockEquivocation returns valid evidence for a proposal conflict:
// same proposer, height, round, different block hash. Nil otherwise.
func (e *Engine) DetectBlockEquivocation(a, b *types.Block) *types.Evidence {
	if a == nil || b == nil || a.Hash() == b.Hash() {
		return nil
	}
	if a.Proposer != b.Proposer || a.Height != b.Height || a.Round != b.Round {
		return nil
	}
	if !a.VerifySig() || !b.VerifySig() {
		return nil
	}
	ev := &types.Evidence{
		Kind:   types.EquivocationBlock,
		Height: a.Height,
		Round:  a.Round,
		BlockA: a,
		BlockB: b,
	}
	if !ev.Validator() {
		return nil
	}
	return ev
}

// DetectVoteEquivocation returns valid evidence for a vote conflict:
// same voter, height, round, different block hash. Nil otherwise.
func (e *Engine) DetectVoteEquivocation(a, b *types.Vote) *types.Evidence {
	if a == nil || b == nil {
		return nil
	}
	if a.Voter != b.Voter || a.Height != b.Height || a.Round != b.Round {
		return nil
	}
	if a.BlockHash == b.BlockHash {
		return nil
	}
	if !a.VerifySig() || !b.VerifySig() {
		return nil
	}
	ev := &types.Evidence{
		Kind:   types.EquivocationVote,
		Height: a.Height,
		Round:  a.Round,
		VoteA:  a,
		VoteB:  b,
	}
	if !ev.Validator() {
		return nil
	}
	return ev
}

// Slot identifies a voting instance within a height (height + round).
type Slot struct {
	Height uint64
	Round  uint32
}

// Tally tracks stake-weighted votes per (height, round) and block hash.
type Tally struct {
	// votes[slot][voter] = vote (one effective vote per voter/slot)
	votes map[Slot]map[types.Address]*types.Vote
}

// NewTally creates an empty tally.
func NewTally() *Tally {
	return &Tally{votes: map[Slot]map[types.Address]*types.Vote{}}
}

// Add records the FIRST vote seen from a voter in a slot and returns any
// prior vote (so callers can construct equivocation evidence).
func (t *Tally) Add(v *types.Vote) *types.Vote {
	sl := Slot{v.Height, v.Round}
	m := t.votes[sl]
	if m == nil {
		m = map[types.Address]*types.Vote{}
		t.votes[sl] = m
	}
	prior := m[v.Voter]
	if prior == nil {
		m[v.Voter] = v
	}
	return prior
}

// HasVoted reports whether a voter already voted in a slot.
func (t *Tally) HasVoted(sl Slot, voter types.Address) bool {
	if m, ok := t.votes[sl]; ok {
		_, voted := m[voter]
		return voted
	}
	return false
}

// WeightFor sums weights of voters endorsing h in a slot.
func (t *Tally) WeightFor(sl Slot, h types.Hash, vs *ValidatorSet) uint64 {
	var sum uint64
	for _, v := range t.votes[sl] {
		if v.BlockHash != h {
			continue
		}
		if val, ok := vs.Get(v.Voter); ok {
			sum += val.Weight
		}
	}
	return sum
}

// VotesAt returns recorded effective votes in a slot, sorted by voter.
func (t *Tally) VotesAt(sl Slot) []*types.Vote {
	m := t.votes[sl]
	out := make([]*types.Vote, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Voter.Cmp(out[j].Voter) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// FinalizedAt returns the block hash reaching at least the threshold
// stake in a slot, or ZeroHash if none.
func (t *Tally) FinalizedAt(sl Slot, vs *ValidatorSet, threshold uint64) types.Hash {
	weights := map[types.Hash]uint64{}
	var bestHash types.Hash
	var bestWeight uint64
	for _, v := range t.votes[sl] {
		val, ok := vs.Get(v.Voter)
		if !ok {
			continue
		}
		w := weights[v.BlockHash] + val.Weight
		weights[v.BlockHash] = w
		if w > bestWeight {
			bestWeight = w
			bestHash = v.BlockHash
		}
	}
	if bestWeight >= threshold {
		return bestHash
	}
	return types.ZeroHash
}

// TxRoot commits an ordered transaction list via a binary Merkle root
// over transaction hashes.
func TxRoot(txs []*types.Tx) types.Hash {
	leaves := make([]types.Hash, len(txs))
	for i, tx := range txs {
		leaves[i] = tx.Hash()
	}
	return merkleHash(leaves)
}

func merkleHash(leaves []types.Hash) types.Hash {
	if len(leaves) == 0 {
		return types.HashFromBytes([]byte("emptytree/v1"))
	}
	level := leaves
	for len(level) > 1 {
		var next []types.Hash
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
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

func sortEvidence(evs []*types.Evidence) {
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j-1].Hash().Cmp(evs[j].Hash()) > 0; j-- {
			evs[j-1], evs[j] = evs[j], evs[j-1]
		}
	}
}
