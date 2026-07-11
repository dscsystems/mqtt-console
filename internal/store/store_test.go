package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/dscsystems/mqtt-console/internal/mqttc"
)

func msg(topic, payload string) mqttc.Message {
	return mqttc.Message{Topic: topic, Payload: []byte(payload), Time: time.Now()}
}

func paths(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Path
	}
	return out
}

func TestAddAndFlatten(t *testing.T) {
	s := New()
	s.Add(msg("home/kitchen/temp", "21"))
	s.Add(msg("home/kitchen/humidity", "40"))
	s.Add(msg("home/hall/temp", "19"))
	s.Add(msg("home/kitchen/temp", "22"))

	if s.TotalMsgs != 4 {
		t.Errorf("TotalMsgs = %d, want 4", s.TotalMsgs)
	}
	if s.Topics() != 3 {
		t.Errorf("Topics = %d, want 3", s.Topics())
	}

	got := paths(s.Flatten(""))
	want := []string{"home", "home/hall", "home/hall/temp", "home/kitchen", "home/kitchen/humidity", "home/kitchen/temp"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Flatten = %v, want %v", got, want)
	}

	n := s.Get("home/kitchen/temp")
	if n == nil || n.MsgCount != 2 || string(n.LastPayload) != "22" {
		t.Fatalf("node = %+v", n)
	}
	if len(n.History) != 2 {
		t.Errorf("history = %d entries, want 2", len(n.History))
	}
	root := s.Get("home")
	if root.SubtreeMsgs != 4 || root.SubtreeTopics != 3 {
		t.Errorf("home aggregates = %d msgs, %d topics; want 4, 3", root.SubtreeMsgs, root.SubtreeTopics)
	}
}

func TestCollapseHidesChildren(t *testing.T) {
	s := New()
	s.Add(msg("a/b/c", "1"))
	s.Add(msg("a/d", "2"))
	s.Get("a/b").Expanded = false
	got := paths(s.Flatten(""))
	want := []string{"a", "a/b", "a/d"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Flatten = %v, want %v", got, want)
	}
}

func TestFilter(t *testing.T) {
	s := New()
	s.Add(msg("home/kitchen/temp", "21"))
	s.Add(msg("home/hall/light", "on"))
	s.Add(msg("garage/door", "closed"))

	got := paths(s.Flatten("temp"))
	want := []string{"home", "home/kitchen", "home/kitchen/temp"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Flatten(temp) = %v, want %v", got, want)
	}

	if len(s.Flatten("nomatch")) != 0 {
		t.Error("expected no rows for non-matching filter")
	}
	// Filter is case-insensitive.
	if len(s.Flatten("GARAGE")) != 2 {
		t.Errorf("Flatten(GARAGE) = %v", paths(s.Flatten("GARAGE")))
	}
}

func TestHistoryLimit(t *testing.T) {
	s := New()
	s.HistoryLimit = 5
	for i := 0; i < 20; i++ {
		s.Add(msg("t", fmt.Sprint(i)))
	}
	n := s.Get("t")
	if len(n.History) != 5 {
		t.Fatalf("history = %d, want 5", len(n.History))
	}
	if string(n.History[4].Payload) != "19" {
		t.Errorf("newest = %s, want 19", n.History[4].Payload)
	}
}

func TestPrune(t *testing.T) {
	s := New()
	s.Add(msg("a/b/c", "1"))
	s.Add(msg("a/b/d", "2"))
	s.Add(msg("a/e", "3"))
	s.Prune(s.Get("a/b"))

	if s.Get("a/b") != nil {
		t.Error("a/b still present after prune")
	}
	if s.Topics() != 1 {
		t.Errorf("Topics = %d, want 1", s.Topics())
	}
	a := s.Get("a")
	if a.SubtreeMsgs != 1 || a.SubtreeTopics != 1 {
		t.Errorf("a aggregates = %d msgs, %d topics; want 1, 1", a.SubtreeMsgs, a.SubtreeTopics)
	}
	got := paths(s.Flatten(""))
	want := []string{"a", "a/e"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Flatten = %v, want %v", got, want)
	}
}

func TestSparkplugAliasLearning(t *testing.T) {
	// Build an NBIRTH with metric "rpm" alias 3 (protobuf wire format,
	// hand-encoded: field 2 (metrics) containing name/alias/datatype/value).
	metric := []byte{
		0x0a, 0x03, 'r', 'p', 'm', // name = "rpm"
		0x10, 0x03, // alias = 3
		0x20, 0x04, // datatype = Int64
		0x58, 0x64, // long_value = 100
	}
	payload := append([]byte{0x08, 0x01}, append([]byte{0x12, byte(len(metric))}, metric...)...) // timestamp=1, metrics

	s := New()
	s.Add(mqttc.Message{Topic: "spBv1.0/g/NBIRTH/edge", Payload: payload, Time: time.Now()})

	aliases := s.SparkplugAliases("spBv1.0/g/NDATA/edge")
	if aliases[3] != "rpm" {
		t.Errorf("aliases = %v, want 3 -> rpm", aliases)
	}
}
