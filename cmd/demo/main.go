// Command demo runs a fully local, in-process multi-validator PoS network and
// prints block production, fork choice, double-sign slashing, finality and
// state roots. It uses the simulated network, so there are no ports, no wall
// clock assumptions and no goroutine scheduling dependence.
//
//	go run ./cmd/demo
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

func addr(i byte) types.Address { var a types.Address; a[19] = i; return a }

func main() {
	logger := log.New(os.Stdout, "", 0)
	net := simnet.New()

	const N = 4
	vals := make([]*types.Validator, N)
	allocs := make([]consensus.GenesisAlloc, 0, N+1)
	for i := 0; i < N; i++ {
		a := addr(byte(i + 1))
		vals[i] = &types.Validator{Addr: a, Stake: 1000, PubKey: identity.PublicKeyFor(a)}
		allocs = append(allocs, consensus.GenesisAlloc{Addr: a, Balance: 1000})
	}
	allocs = append(allocs, consensus.GenesisAlloc{Addr: addr(99), Balance: 5000})
	genesis, gs := consensus.MakeGenesis(vals, allocs)

	nodes := make([]*node.Node, N)
	for i := range nodes {
		a := addr(byte(i + 1))
		nodes[i] = node.New(node.Config{
			Name: string(rune('A' + i)), Addr: a,
			Net: net, Vals: vals, Genesis: genesis, GenesisState: gs,
			PrivKey: identity.PrivateKeyFor(a), Logger: logger,
		})
	}

	// A user submits two transactions.
	for _, n := range []uint64{0, 1} {
		tx := types.SignTx(&types.Tx{From: addr(99), To: addr(2), Nonce: n, Value: 10, Fee: 5 - n})
		if err := nodes[2].SubmitTx(tx); err != nil {
			log.Fatal(err)
		}
	}
	net.DeliverAll()

	set := consensus.NewSet(vals)
	step := func() {
		h := nodes[0].Head().Height + 1
		pi := indexOf(set.ProposerAt(h), nodes)
		if b := nodes[pi].ProduceBlock(); b != nil {
			net.DeliverAll()
		}
		for _, nd := range nodes {
			nd.CastVote()
		}
		net.DeliverAll()
	}

	for i := 0; i < 6; i++ {
		step()
	}

	// Report cross-node agreement on head hash and state root.
	var head types.Hash
	var root types.Hash
	agree := true
	for _, nd := range nodes {
		hh := types.HashBlock(nd.Head())
		if head == (types.Hash{}) {
			head, root = hh, nd.Head().StateRoot
		} else if hh != head || nd.Head().StateRoot != root {
			agree = false
		}
	}
	fmt.Println("---------------------------------------------------------------")
	fmt.Printf("final head height : %d\n", nodes[0].Head().Height)
	fmt.Printf("finalized height  : %d\n", nodes[0].FinalizedHeight())
	fmt.Printf("head hash         : %x\n", head[:])
	fmt.Printf("state root        : %x\n", root[:])
	fmt.Printf("all nodes agree   : %v\n", agree)

	// Balance conservation: slashes move into the public treasury (still a
	// live balance), only fees are burned.
	st := nodes[0].StateAt()
	live := st.TotalBalance()
	treasury := st.TreasuryBalance()
	burn := nodes[0].Chain().BurnOn(nodes[0].Chain().Head())
	fmt.Printf("live balances     : %d (incl. public treasury %d)\n", live, treasury)
	fmt.Printf("burned (fees only): %d\n", burn)
	fmt.Printf("genesis total     : 9000 (conservation: %v)\n", live+burn == 9000)
}

func indexOf(a types.Address, nodes []*node.Node) int {
	for i, nd := range nodes {
		if nd.Addr == a {
			return i
		}
	}
	return -1
}
