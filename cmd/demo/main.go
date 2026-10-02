// Command demo runs a fully local, in-process multi-validator PoS network and
// prints block production, fork choice, double-sign slashing, finality and
// state roots. It uses the simulated network, so there are no ports, no wall
// clock assumptions and no goroutine scheduling dependence.
//
//	go run ./cmd/demo
package main

import (
	"crypto/ed25519"
	"fmt"
	"log"
	"os"

	"github.com/h3yow/pos-consensus-ledger-go/consensus"
	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
	"github.com/h3yow/pos-consensus-ledger-go/state"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// validatorKey deterministically derives validator i's key pair from a seed,
// standing in for "each node starts holding its own private key".
func validatorKey(i byte) (types.Address, ed25519.PrivateKey) {
	priv := types.GenerateKey([]byte{0x76, 'v', 'a', 'l', i})
	return types.AddressOf(priv.Public().(ed25519.PublicKey)), priv
}

// userKey derives a non-validator account key (transactions keep the
// simulation tx signature, so here only the address is used).
func userKey(tag byte) types.Address {
	priv := types.GenerateKey([]byte{0x75, 'u', 's', 'r', tag})
	return types.AddressOf(priv.Public().(ed25519.PublicKey))
}

func main() {
	logger := log.New(os.Stdout, "", 0)
	net := simnet.New()

	const N = 4
	type vinfo struct {
		addr types.Address
		priv ed25519.PrivateKey
	}
	infos := make([]vinfo, N)
	vals := make([]*types.Validator, N)
	allocs := make([]consensus.GenesisAlloc, 0, N+1)
	for i := 0; i < N; i++ {
		a, priv := validatorKey(byte(i + 1))
		infos[i] = vinfo{addr: a, priv: priv}
		vals[i] = &types.Validator{
			Addr: a, Stake: 1000,
			PubKey: priv.Public().(ed25519.PublicKey),
		}
		allocs = append(allocs, consensus.GenesisAlloc{Addr: a, Balance: 1000})
	}
	user := userKey(99)
	allocs = append(allocs, consensus.GenesisAlloc{Addr: user, Balance: 5000})
	genesis, gs := consensus.MakeGenesis(vals, allocs)

	nodes := make([]*node.Node, N)
	for i := range nodes {
		nodes[i] = node.New(node.Config{
			Name: string(rune('A' + i)), Addr: infos[i].addr, PrivKey: infos[i].priv,
			Net: net, Vals: vals, Genesis: genesis, GenesisState: gs, Logger: logger,
		})
	}

	// A user submits two transactions.
	for _, n := range []uint64{0, 1} {
		tx := types.SignTx(&types.Tx{From: user, To: infos[1].addr, Nonce: n, Value: 10, Fee: 5 - n})
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

	// Conservation: balances incl. the protocol public account + burned fees.
	live := nodes[0].StateAt().TotalBalance()
	feeBurn := nodes[0].Chain().FeeBurnOn(nodes[0].Chain().Head())
	common := nodes[0].StateAt().GetBalance(state.ProtocolAccount)
	fmt.Printf("live balances     : %d (protocol public account: %d)\n", live, common)
	fmt.Printf("burned fees       : %d\n", feeBurn)
	fmt.Printf("genesis total     : 9000 (conservation: %v)\n", live+feeBurn == 9000)
}

func indexOf(a types.Address, nodes []*node.Node) int {
	for i, nd := range nodes {
		if nd.Addr == a {
			return i
		}
	}
	return -1
}
