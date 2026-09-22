package test_test

import "github.com/h3yow/pos-consensus-ledger-go/internal/consensus"

func slotOf(height uint64, round uint32) consensus.Slot {
	return consensus.Slot{Height: height, Round: round}
}
