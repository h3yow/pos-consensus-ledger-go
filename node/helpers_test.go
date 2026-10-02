package node_test

import (
	"io"
	"log"

	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

type nodeRef struct {
	nd   *node.Node
	name string
}

func newNode(c *cluster, nm string, a types.Address) *node.Node {
	var w io.Writer = &tWriter{t: c.t}
	return node.New(node.Config{
		Name: nm, Addr: a, Net: c.net, Vals: c.vals,
		Genesis: c.g, GenesisState: c.gs,
		PrivKey: identity.PrivateKeyFor(a),
		Logger:  log.New(w, "", 0),
	})
}

// signVoteAs signs a vote with the derived private key of voter, exactly as
// the holder of that validator's key could do (e.g. an equivocating twin).
func signVoteAs(voter types.Address, height uint64, block types.Hash) *types.Vote {
	return identity.SignVote(&types.Vote{Height: height, Voter: voter, BlockHash: block},
		identity.PrivateKeyFor(voter))
}
