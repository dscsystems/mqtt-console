// Package mqttc provides a version-agnostic MQTT client abstraction over
// eclipse/paho.mqtt.golang (MQTT 3.1/3.1.1) and eclipse/paho.golang (MQTT 5).
package mqttc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Version selects the MQTT protocol version.
type Version int

const (
	V31  Version = 3 // MQTT 3.1
	V311 Version = 4 // MQTT 3.1.1
	V5   Version = 5 // MQTT 5.0
)

func (v Version) String() string {
	switch v {
	case V31:
		return "3.1"
	case V311:
		return "3.1.1"
	case V5:
		return "5.0"
	}
	return fmt.Sprintf("unknown(%d)", int(v))
}

// TLSOptions configures the TLS layer for ssl/tls/mqtts/wss connections.
type TLSOptions struct {
	CAFile   string
	CertFile string
	KeyFile  string
	Insecure bool // skip server certificate verification
}

// Will is the MQTT last-will message registered at connect time.
type Will struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// Options describes a broker connection.
type Options struct {
	URL            string // tcp:// ssl:// mqtt:// mqtts:// ws:// wss://
	ClientID       string
	Username       string
	Password       string
	Version        Version
	KeepAlive      time.Duration
	ConnectTimeout time.Duration
	CleanStart     bool
	TLS            TLSOptions
	Will           *Will
}

// Message is a received MQTT publication.
type Message struct {
	Topic       string
	Payload     []byte
	QoS         byte
	Retained    bool
	Duplicate   bool
	Time        time.Time
	ContentType string      // MQTT 5 only
	UserProps   [][2]string // MQTT 5 only
}

// EventKind classifies connection lifecycle events.
type EventKind int

const (
	EventConnected EventKind = iota
	EventDisconnected
	EventReconnecting
	EventError
)

// Event reports a connection state change.
type Event struct {
	Kind   EventKind
	Err    error
	Detail string
}

// Subscription is an active topic filter.
type Subscription struct {
	Filter string
	QoS    byte
}

// Client is the protocol-version-agnostic MQTT client.
type Client interface {
	// Connect establishes the connection, blocking until connected or ctx expires.
	Connect(ctx context.Context) error
	// Disconnect closes the connection and both channels.
	Disconnect()
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
	Subscribe(ctx context.Context, filter string, qos byte) error
	Unsubscribe(ctx context.Context, filter string) error
	// Messages delivers received publications. If the consumer falls behind,
	// messages are dropped and counted (see Dropped) rather than blocking the
	// network loop.
	Messages() <-chan Message
	// Events delivers connection lifecycle events.
	Events() <-chan Event
	// Subscriptions lists active topic filters, sorted.
	Subscriptions() []Subscription
	// Dropped is the number of messages discarded because the consumer was slow.
	Dropped() uint64
	ServerURL() string
	ProtocolVersion() Version
}

// New builds a Client for the requested protocol version.
func New(o Options) (Client, error) {
	if o.URL == "" {
		return nil, fmt.Errorf("broker URL is required")
	}
	if o.Version == 0 {
		o.Version = V311
	}
	if o.KeepAlive <= 0 {
		o.KeepAlive = 30 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 15 * time.Second
	}
	if o.ClientID == "" {
		o.ClientID = fmt.Sprintf("mqtt-console-%08x", time.Now().UnixNano()&0xffffffff)
	}
	switch o.Version {
	case V31, V311:
		return newV3(o), nil
	case V5:
		return newV5(o), nil
	default:
		return nil, fmt.Errorf("unsupported MQTT version %d (use 3, 4 or 5)", int(o.Version))
	}
}

// normalizeURL fills in a default scheme and port and maps scheme aliases.
func normalizeURL(raw string, aliases map[string]string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "tcp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid broker URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if mapped, ok := aliases[scheme]; ok {
		scheme = mapped
	}
	u.Scheme = scheme
	if u.Port() == "" {
		switch scheme {
		case "tcp", "mqtt":
			u.Host = u.Host + ":1883"
		case "ssl", "tls", "mqtts":
			u.Host = u.Host + ":8883"
		case "ws":
			u.Host = u.Host + ":80"
		case "wss":
			u.Host = u.Host + ":443"
		}
	}
	return u, nil
}

// isSecureScheme reports whether the URL scheme implies TLS.
func isSecureScheme(scheme string) bool {
	switch scheme {
	case "ssl", "tls", "mqtts", "wss":
		return true
	}
	return false
}

// buildTLSConfig assembles a *tls.Config from TLSOptions. Returns nil when no
// TLS-related option is set, letting the transport default apply.
func buildTLSConfig(o Options, scheme string) (*tls.Config, error) {
	t := o.TLS
	if t.CAFile == "" && t.CertFile == "" && t.KeyFile == "" && !t.Insecure {
		if isSecureScheme(scheme) {
			return &tls.Config{MinVersion: tls.VersionTLS12}, nil
		}
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.Insecure} //nolint:gosec // user-requested
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in CA file %s", t.CAFile)
		}
		cfg.RootCAs = pool
	}
	if t.CertFile != "" || t.KeyFile != "" {
		if t.CertFile == "" || t.KeyFile == "" {
			return nil, fmt.Errorf("client certificate requires both --cert and --key")
		}
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// subTracker keeps active subscriptions so they can be restored on reconnect
// and listed in the UI.
type subTracker struct {
	mu   sync.Mutex
	subs map[string]byte
}

func newSubTracker() *subTracker { return &subTracker{subs: make(map[string]byte)} }

func (s *subTracker) add(filter string, qos byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[filter] = qos
}

func (s *subTracker) remove(filter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, filter)
}

func (s *subTracker) list() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Subscription, 0, len(s.subs))
	for f, q := range s.subs {
		out = append(out, Subscription{Filter: f, QoS: q})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filter < out[j].Filter })
	return out
}

// chanPair owns the outbound channels shared by both client implementations.
type chanPair struct {
	msgs    chan Message
	events  chan Event
	dropped atomic.Uint64
	closed  atomic.Bool
	once    sync.Once
}

func newChanPair() *chanPair {
	return &chanPair{
		msgs:   make(chan Message, 4096),
		events: make(chan Event, 64),
	}
}

// sendMsg forwards without blocking; drops (and counts) when the buffer is full.
func (c *chanPair) sendMsg(m Message) {
	if c.closed.Load() {
		return
	}
	select {
	case c.msgs <- m:
	default:
		c.dropped.Add(1)
	}
}

func (c *chanPair) sendEvent(e Event) {
	if c.closed.Load() {
		return
	}
	select {
	case c.events <- e:
	default:
	}
}

func (c *chanPair) close() {
	c.once.Do(func() {
		c.closed.Store(true)
		// Give in-flight handlers a moment before closing.
		go func() {
			time.Sleep(100 * time.Millisecond)
			close(c.msgs)
			close(c.events)
		}()
	})
}
