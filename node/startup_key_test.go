package node_test

import (
	"io"
	"log"
	"testing"

	"github.com/h3yow/pos-consensus-ledger-go/identity"
	"github.com/h3yow/pos-consensus-ledger-go/node"
	"github.com/h3yow/pos-consensus-ledger-go/simnet"
)

// TestNodeRejectsWrongPrivateKey: a node started for a validator address but
// holding a DIFFERENT private key must refuse to start — it could never
// produce signatures other nodes accept, and must not run mis-authenticated.
func TestNodeRejectsWrongPrivateKey(t *testing.T) {
	c := newCluster(t, 4)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when private key does not match published key")
		} else {
			t.Logf("decision: startup with mismatched key rejected: %v", r)
		}
	}()

	var w io.Writer = &tWriter{t: t}
	_ = node.New(node.Config{
		Name:    "ROGUE",
		Addr:    addr(1),                                   // claims to be validator A
		Net:     simnet.New(),                              // isolated switch
		Vals:    c.vals,
		Genesis: c.g, GenesisState: c.gs,
		PrivKey: identity.PrivateKeyFor(addr(2)), // ...but holds validator B's key
		Logger:  log.New(w, "", 0),
	})
}

// TestNodeAcceptsCorrectPrivateKey is the positive counterpart and is
// already exercised by every cluster test; assert it once explicitly.
func TestNodeAcceptsCorrectPrivateKey(t *testing.T) {
	c := newCluster(t, 4)
	nd := newNode(c, "OK1", addr(1))
	if nd == nil {
		t.Fatal("node with matching key should start")
	}
	t.Log("decision: node with key matching its published public key starts")
}
