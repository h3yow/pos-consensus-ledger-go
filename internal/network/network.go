// Package network provides an in-process simulated network connecting
// local validator nodes. Delivery is explicit (DeliverOne /
// DeliverRound / DeliverAll) instead of timer-driven, so tests never
// depend on wall-clock timing or real sockets. A node can be
// disconnected (partitioned, its incoming messages held) and restored,
// and messages can be redelivered to simulate duplicate broadcasts.
package network

import (
	"sort"
	"sync"

	"github.com/h3yow/pos-consensus-ledger-go/internal/types"
)

// Handler receives delivered messages (implemented by *node.Node).
type Handler interface {
	ID() string
	HandleMessage(m types.Message)
}

// SimNetwork is an in-memory message switch with per-node FIFO queues.
type SimNetwork struct {
	mu        sync.Mutex
	handlers  map[string]Handler
	queues    map[string][]types.Message
	connected map[string]bool
}

// New creates an empty simulated network.
func New() *SimNetwork {
	return &SimNetwork{
		handlers:  map[string]Handler{},
		queues:    map[string][]types.Message{},
		connected: map[string]bool{},
	}
}

// Register connects a node to the network.
func (n *SimNetwork) Register(h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handlers[h.ID()] = h
	n.queues[h.ID()] = nil
	n.connected[h.ID()] = true
}

// Broadcast enqueues a message for every connected node except the
// sender. While a receiver is disconnected its messages are still held
// in its queue and become deliverable after Restore.
func (n *SimNetwork) Broadcast(m types.Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := make([]string, 0, len(n.handlers))
	for id := range n.handlers {
		if id == m.From {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !n.connected[id] {
			// Hold messages for partitioned receivers so that after
			// recovery they observe the delayed broadcasts.
		}
		n.queues[id] = append(n.queues[id], m)
	}
}

// popLocked removes and returns the oldest queued message for id.
func (n *SimNetwork) popLocked(id string) (types.Message, bool) {
	q := n.queues[id]
	if len(q) == 0 {
		return types.Message{}, false
	}
	m := q[0]
	n.queues[id] = q[1:]
	return m, true
}

// DeliverOne delivers the oldest queued message to a connected node.
// Messages to a currently disconnected node are skipped (kept queued).
// It returns false when nothing could be delivered.
func (n *SimNetwork) DeliverOne(nodeID string) bool {
	n.mu.Lock()
	h, ok := n.handlers[nodeID]
	if !ok || !n.connected[nodeID] || len(n.queues[nodeID]) == 0 {
		n.mu.Unlock()
		return false
	}
	q := n.queues[nodeID]
	m := q[0]
	n.queues[nodeID] = q[1:]
	n.mu.Unlock()

	// Deliver WITHOUT holding the switch lock: handlers may broadcast
	// further messages, which must be able to re-enter the network.
	h.HandleMessage(m)
	return true
}

// nodeIDsLocked returns handler ids in ascending order.
func (n *SimNetwork) nodeIDsLocked() []string {
	ids := make([]string, 0, len(n.handlers))
	for id := range n.handlers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DeliverRound delivers at most one queued message per node, once each,
// in fixed ascending node-id order, returning the delivered count.
func (n *SimNetwork) DeliverRound() int {
	delivered := 0
	for _, id := range n.snapshotIDs() {
		if n.DeliverOne(id) {
			delivered++
		}
	}
	return delivered
}

// DeliverAll repeatedly delivers rounds until every connected queue is
// empty (including messages generated while processing), returning the
// total number of deliveries.
func (n *SimNetwork) DeliverAll() int {
	total := 0
	for {
		d := n.DeliverRound()
		total += d
		if d == 0 {
			return total
		}
	}
}

func (n *SimNetwork) snapshotIDs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.nodeIDsLocked()
}

// Pending reports the queued message count for a node.
func (n *SimNetwork) Pending(nodeID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.queues[nodeID])
}

// Disconnect marks a node partitioned: new broadcasts are still held but
// delivery to it pauses until Restore.
func (n *SimNetwork) Disconnect(nodeID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.connected[nodeID] = false
}

// Restore reconnects a node; held messages remain queued and flush on
// the next Deliver* call.
func (n *SimNetwork) Restore(nodeID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.handlers[nodeID]; ok {
		n.connected[nodeID] = true
	}
}

// IsConnected reports the current connection status.
func (n *SimNetwork) IsConnected(nodeID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connected[nodeID]
}

// Redeliver duplicates the oldest queued message for a node at the tail,
// simulating a duplicate broadcast. Returns false if the queue is empty.
func (n *SimNetwork) Redeliver(nodeID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	q := n.queues[nodeID]
	if len(q) == 0 {
		return false
	}
	n.queues[nodeID] = append(q, q[0])
	return true
}
