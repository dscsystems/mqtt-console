// Package store maintains the live topic tree: per-topic message history,
// counters, message rates and Sparkplug alias maps learned from BIRTH
// messages.
package store

import (
	"strings"
	"time"

	"github.com/dscsystems/mqtt-console/internal/decode/sparkplug"
	"github.com/dscsystems/mqtt-console/internal/mqttc"
)

// DefaultHistoryLimit is the per-topic message history depth.
const DefaultHistoryLimit = 100

// Node is one segment in the topic tree. Nodes that have received messages
// carry history; intermediate nodes only aggregate.
type Node struct {
	Name     string // last topic segment
	Path     string // full topic up to and including this segment
	Parent   *Node
	Depth    int
	Expanded bool

	children map[string]*Node
	order    []*Node // children sorted by name

	MsgCount      int             // messages on this exact topic
	SubtreeMsgs   int             // messages in this subtree (including self)
	SubtreeTopics int             // distinct message-bearing topics in subtree
	History       []mqttc.Message // newest last, capped at HistoryLimit
	LastPayload   []byte
}

// HasData reports whether this exact topic has received messages.
func (n *Node) HasData() bool { return n.MsgCount > 0 }

// Children returns the sorted child list.
func (n *Node) Children() []*Node { return n.order }

// Last returns the most recent message, or nil.
func (n *Node) Last() *mqttc.Message {
	if len(n.History) == 0 {
		return nil
	}
	return &n.History[len(n.History)-1]
}

// rateCounter tracks per-second message counts over a sliding window.
type rateCounter struct {
	buckets [11]int
	times   [11]int64
}

func (r *rateCounter) hit(now int64) {
	i := now % int64(len(r.buckets))
	if r.times[i] != now {
		r.times[i] = now
		r.buckets[i] = 0
	}
	r.buckets[i]++
}

// rate returns messages per second averaged over the last 10 complete seconds.
func (r *rateCounter) rate(now int64) float64 {
	sum := 0
	for i := range r.buckets {
		if r.times[i] >= now-10 && r.times[i] < now {
			sum += r.buckets[i]
		}
	}
	return float64(sum) / 10
}

// Store is the message database backing the UI. It is not goroutine-safe;
// the Bubble Tea update loop is its only caller.
type Store struct {
	Root         *Node
	TotalMsgs    int
	TotalBytes   int64
	HistoryLimit int

	rc      rateCounter
	topics  int
	aliases map[string]map[uint64]string // sparkplug EdgeKey → alias→name
}

// New creates an empty store.
func New() *Store {
	return &Store{
		Root:         &Node{Name: "", Path: "", Expanded: true, children: map[string]*Node{}},
		HistoryLimit: DefaultHistoryLimit,
		aliases:      map[string]map[uint64]string{},
	}
}

// Topics returns the number of distinct topics that have received messages.
func (s *Store) Topics() int { return s.topics }

// Rate returns the recent ingest rate in messages per second.
func (s *Store) Rate() float64 { return s.rc.rate(time.Now().Unix()) }

// Add records a message and returns the node it landed on.
func (s *Store) Add(m mqttc.Message) *Node {
	s.TotalMsgs++
	s.TotalBytes += int64(len(m.Payload))
	s.rc.hit(m.Time.Unix())

	node := s.ensure(m.Topic)
	firstData := !node.HasData()
	node.MsgCount++
	node.LastPayload = m.Payload
	node.History = append(node.History, m)
	if len(node.History) > s.HistoryLimit {
		node.History = node.History[len(node.History)-s.HistoryLimit:]
	}
	for p := node; p != nil; p = p.Parent {
		p.SubtreeMsgs++
		if firstData {
			p.SubtreeTopics++
		}
	}
	if firstData {
		s.topics++
	}

	s.learnSparkplugAliases(m)
	return node
}

// learnSparkplugAliases decodes BIRTH messages so alias-only DATA messages
// can be rendered with metric names later.
func (s *Store) learnSparkplugAliases(m mqttc.Message) {
	if !sparkplug.IsSparkplugTopic(m.Topic) {
		return
	}
	ti, ok := sparkplug.ParseTopic(m.Topic)
	if !ok || !ti.IsBirth() {
		return
	}
	p, err := sparkplug.Decode(m.Payload)
	if err != nil {
		return
	}
	found := sparkplug.HarvestAliases(p)
	if len(found) == 0 {
		return
	}
	key := ti.EdgeKey()
	dst := s.aliases[key]
	if dst == nil {
		dst = map[uint64]string{}
		s.aliases[key] = dst
	}
	for a, n := range found {
		dst[a] = n
	}
}

// SparkplugAliases returns the learned alias map for a topic's edge node.
func (s *Store) SparkplugAliases(topic string) map[uint64]string {
	ti, ok := sparkplug.ParseTopic(topic)
	if !ok {
		return nil
	}
	if m := s.aliases[ti.EdgeKey()]; m != nil {
		return m
	}
	// DDATA aliases may have been announced in the NBIRTH (no device part).
	ti.DeviceID = ""
	return s.aliases[ti.EdgeKey()]
}

// ensure walks (and creates) the node path for a topic.
func (s *Store) ensure(topic string) *Node {
	node := s.Root
	if topic == "" {
		return node
	}
	path := ""
	for _, seg := range strings.Split(topic, "/") {
		if path == "" {
			path = seg
		} else {
			path = path + "/" + seg
		}
		child, ok := node.children[seg]
		if !ok {
			child = &Node{
				Name: seg, Path: path, Parent: node, Depth: node.Depth + 1,
				Expanded: true, children: map[string]*Node{},
			}
			node.children[seg] = child
			node.insertSorted(child)
		}
		node = child
	}
	return node
}

func (n *Node) insertSorted(child *Node) {
	lo, hi := 0, len(n.order)
	for lo < hi {
		mid := (lo + hi) / 2
		if n.order[mid].Name < child.Name {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	n.order = append(n.order, nil)
	copy(n.order[lo+1:], n.order[lo:])
	n.order[lo] = child
}

// Prune removes a subtree from the store (view-side only; no broker action).
func (s *Store) Prune(node *Node) {
	if node == nil || node.Parent == nil {
		return
	}
	// Roll aggregates back up the ancestor chain.
	for p := node.Parent; p != nil; p = p.Parent {
		p.SubtreeMsgs -= node.SubtreeMsgs
		p.SubtreeTopics -= node.SubtreeTopics
	}
	s.topics -= node.SubtreeTopics
	delete(node.Parent.children, node.Name)
	for i, c := range node.Parent.order {
		if c == node {
			node.Parent.order = append(node.Parent.order[:i], node.Parent.order[i+1:]...)
			break
		}
	}
}

// Get returns the node for an exact topic, or nil.
func (s *Store) Get(topic string) *Node {
	node := s.Root
	if topic == "" {
		return node
	}
	for _, seg := range strings.Split(topic, "/") {
		child, ok := node.children[seg]
		if !ok {
			return nil
		}
		node = child
	}
	return node
}

// SetExpandedAll expands or collapses every node.
func (s *Store) SetExpandedAll(expanded bool) {
	var walk func(*Node)
	walk = func(n *Node) {
		n.Expanded = expanded
		for _, c := range n.order {
			walk(c)
		}
	}
	walk(s.Root)
	s.Root.Expanded = true
}

// Flatten returns the visible rows of the tree in display order, honouring
// per-node expansion. With a non-empty filter (case-insensitive substring on
// the full path), only matching topics and their ancestors are returned and
// expansion state is ignored.
func (s *Store) Flatten(filter string) []*Node {
	var out []*Node
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		var walk func(*Node)
		walk = func(n *Node) {
			if n != s.Root {
				out = append(out, n)
				if !n.Expanded {
					return
				}
			}
			for _, c := range n.order {
				walk(c)
			}
		}
		walk(s.Root)
		return out
	}

	var walk func(n *Node) bool // returns whether subtree matched
	walk = func(n *Node) bool {
		self := n != s.Root && strings.Contains(strings.ToLower(n.Path), filter)
		mark := len(out)
		if n != s.Root {
			out = append(out, n)
		}
		childMatched := false
		for _, c := range n.order {
			if walk(c) {
				childMatched = true
			}
		}
		if n == s.Root {
			return childMatched
		}
		if !self && !childMatched {
			out = out[:mark] // drop this node and its subtree
			return false
		}
		return true
	}
	walk(s.Root)
	return out
}
