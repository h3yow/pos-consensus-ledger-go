// Package simnet provides an in-process, fully deterministic simulated network
// between local nodes. Messages are queued and explicitly delivered, so tests
// never depend on real networking, goroutine scheduling or wall-clock timing.
package simnet

import (
	"github.com/h3yow/pos-consensus-ledger-go/types"
)

// MessageKind enumerates gossiped payloads.
type MessageKind int

const (
	MsgBlock MessageKind = iota
	MsgVote
	MsgTx
)

func (k MessageKind) String() string {
	switch k {
	case MsgBlock:
		return "BLOCK"
	case MsgVote:
		return "VOTE"
	case MsgTx:
		return "TX"
	default:
		return "?"
	}
}

// Message is an envelope delivered between nodes.
type Message struct {
	Kind  MessageKind
	From  string
	Block *types.Block
	Vote  *types.Vote
	Tx    *types.Tx
}

// Handler receives messages addressed to a node.
type Handler interface {
	Receive(m Message)
}

// Network is the shared in-process switch with per-node FIFO queues.
type Network struct {
	nodes  map[string]Handler
	queues map[string][]Message
	order  []string // registration order for deterministic draining
}

// New creates an empty simulated network.
func New() *Network {
	return &Network{nodes: map[string]Handler{}, queues: map[string][]Message{}}
}

// Register connects a named node to the network.
func (n *Network) Register(name string, h Handler) {
	if _, ok := n.nodes[name]; !ok {
		n.order = append(n.order, name)
	}
	n.nodes[name] = h
	if _, ok := n.queues[name]; !ok {
		n.queues[name] = nil
	}
}

// Gossip queues a message for every registered node except the sender.
// Because it is just an in-process enqueue, "network partitions", delays,
// reordering and duplicate broadcasts are all expressed by controlling
// when/if Deliver is called.
func (n *Network) Gossip(m Message) {
	for _, name := range n.order {
		if name == m.From {
			continue
		}
		n.queues[name] = append(n.queues[name], m)
	}
}

// GossipTo behaves like Gossip but reaches only the listed recipients
// (useful to simulate partial propagation / partitions).
func (n *Network) GossipTo(m Message, recipients ...string) {
	for _, name := range recipients {
		if name == m.From {
			continue
		}
		if _, ok := n.nodes[name]; ok {
			n.queues[name] = append(n.queues[name], m)
		}
	}
}

// SendTo queues a message for one specific node.
func (n *Network) SendTo(to string, m Message) {
	if _, ok := n.nodes[to]; ok {
		n.queues[to] = append(n.queues[to], m)
	}
}

// Duplicate re-queues the pending message at index idx for a node,
// simulating a duplicate broadcast.
func (n *Network) Duplicate(name string, idx int) bool {
	q := n.queues[name]
	if idx < 0 || idx >= len(q) {
		return false
	}
	q = append(q, q[idx])
	n.queues[name] = q
	return true
}

// Requeue moves a queued message to the back (simulating a delayed arrival).
func (n *Network) Requeue(name string, idx int) bool {
	q := n.queues[name]
	if idx < 0 || idx >= len(q) {
		return false
	}
	m := q[idx]
	n.queues[name] = append(append(q[:idx], q[idx+1:]...), m)
	return true
}

// Drop discards a queued message (simulated packet loss / partition).
func (n *Network) Drop(name string, idx int) bool {
	q := n.queues[name]
	if idx < 0 || idx >= len(q) {
		return false
	}
	n.queues[name] = append(q[:idx], q[idx+1:]...)
	return true
}

// Pending reports queued message count for a node.
func (n *Network) Pending(name string) int { return len(n.queues[name]) }

// Queued returns a copy of the queued messages for inspection in tests.
func (n *Network) Queued(name string) []Message {
	out := make([]Message, len(n.queues[name]))
	copy(out, n.queues[name])
	return out
}

// DeliverOne delivers the next queued message to one node (FIFO).
// Returns false when that node has no queued messages.
func (n *Network) DeliverOne(name string) bool {
	q := n.queues[name]
	if len(q) == 0 {
		return false
	}
	m := q[0]
	n.queues[name] = q[1:]
	n.nodes[name].Receive(m)
	return true
}

// DeliverN delivers up to k queued messages to a node.
func (n *Network) DeliverN(name string, k int) int {
	delivered := 0
	for i := 0; i < k; i++ {
		if !n.DeliverOne(name) {
			break
		}
		delivered++
	}
	return delivered
}

// DeliverAll drains every node queue in registration order, repeatedly,
// until no queues remain. New messages generated during delivery are also
// drained (fixed point), and the traversal order is deterministic.
func (n *Network) DeliverAll() {
	for {
		progress := false
		for _, name := range n.order {
			if len(n.queues[name]) > 0 {
				n.DeliverOne(name)
				progress = true
			}
		}
		if !progress {
			return
		}
	}
}
