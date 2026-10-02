package node_test

import (
	"io"
	"log"

	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

type nodeRef struct {
	nd   *node.Node
	name string
}

func newNode(c *cluster, nm string, a types.Address) *node.Node {
	priv, ok := c.keys[a]
	if !ok {
		c.t.Fatalf("no private key configured for %x", a[:8])
	}
	var w io.Writer = &tWriter{t: c.t}
	return node.New(node.Config{
		Name: nm, Addr: a, PrivKey: priv, Net: c.net, Vals: c.vals,
		Genesis: c.g, GenesisState: c.gs,
		Logger: log.New(w, "", 0),
	})
}
