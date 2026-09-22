// Package network provides an in-process simulated network connecting
// local nodes. Delivery is explicit (DeliverOne / DeliverAll) so tests
// never depend on timing or real sockets. Links can be cut/restored
// (partition + recovery) and duplicate delivery is supported.
package network

import (
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Handler receives delivered messages (typically a *node.Node).
type Handler interface {
	ID() string
	HandleMessage(m types.Message)
}

// SimNetwork is an in-memory message switch with per-node FIFO queues.
type SimNetwork struct {
	mu        sync.Mutex
	handlers  map[string]Handler
	queues    map[string][]types.Message
	connected map[string]bool // node online/offline (partition)
	seq       int
}

// New creates an empty simulated network.
func New() *SimNetwork { panic("not implemented") }

// Register connects a node to the network.
func (n *SimNetwork) Register(h Handler) { panic("not implemented") }

// Broadcast enqueues a message to every connected node except the sender
// (and except explicitly disconnected receivers). Messages to
// disconnected receivers are held until Restore.
func (n *SimNetwork) Broadcast(m types.Message) { panic("not implemented") }

// DeliverOne delivers the oldest queued message for a node.
// It returns false when the queue is empty.
func (n *SimNetwork) DeliverOne(nodeID string) bool { panic("not implemented") }

// DeliverRound delivers one queued message per node, once, in a fixed
// node-id order, returning how many messages were delivered.
func (n *SimNetwork) DeliverRound() int { panic("not implemented") }

// DeliverAll repeatedly delivers rounds until every queue is empty.
func (n *SimNetwork) DeliverAll() int { panic("not implemented") }

// Pending reports the queued message count for a node.
func (n *SimNetwork) Pending(nodeID string) int { panic("not implemented") }

// Disconnect holds a node's incoming messages (partition receiver).
func (n *SimNetwork) Disconnect(nodeID string) { panic("not implemented") }

// Restore re-enables delivery for a node; held messages remain queued.
func (n *SimNetwork) Restore(nodeID string) { panic("not implemented") }

// Redeliver duplicates the oldest queued message for a node at the tail,
// simulating duplicate broadcast / replay.
func (n *SimNetwork) Redeliver(nodeID string) bool { panic("not implemented") }
