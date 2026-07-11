package mqttc

import (
	"context"
	"fmt"
	"sync"
	"time"

	paho3 "github.com/eclipse/paho.mqtt.golang"
)

// v3Client wraps eclipse/paho.mqtt.golang for MQTT 3.1 and 3.1.1.
type v3Client struct {
	opts      Options
	client    paho3.Client
	subs      *subTracker
	ch        *chanPair
	url       string
	connected chan struct{} // closed once the first OnConnect has run
	connOnce  sync.Once
}

var v3SchemeAliases = map[string]string{
	"mqtt":  "tcp",
	"mqtts": "ssl",
	"tls":   "ssl",
}

func newV3(o Options) *v3Client {
	return &v3Client{
		opts:      o,
		subs:      newSubTracker(),
		ch:        newChanPair(),
		connected: make(chan struct{}),
	}
}

func (c *v3Client) Connect(ctx context.Context) error {
	u, err := normalizeURL(c.opts.URL, v3SchemeAliases)
	if err != nil {
		return err
	}
	c.url = u.String()

	po := paho3.NewClientOptions()
	po.AddBroker(u.String())
	po.SetClientID(c.opts.ClientID)
	if c.opts.Username != "" {
		po.SetUsername(c.opts.Username)
	}
	if c.opts.Password != "" {
		po.SetPassword(c.opts.Password)
	}
	po.SetCleanSession(c.opts.CleanStart)
	po.SetKeepAlive(c.opts.KeepAlive)
	po.SetConnectTimeout(c.opts.ConnectTimeout)
	po.SetAutoReconnect(true)
	po.SetMaxReconnectInterval(30 * time.Second)
	po.SetOrderMatters(false)
	if c.opts.Version == V31 {
		po.SetProtocolVersion(3)
	} else {
		po.SetProtocolVersion(4)
	}
	tlsCfg, err := buildTLSConfig(c.opts, u.Scheme)
	if err != nil {
		return err
	}
	if tlsCfg != nil {
		po.SetTLSConfig(tlsCfg)
	}
	if w := c.opts.Will; w != nil {
		po.SetBinaryWill(w.Topic, w.Payload, w.QoS, w.Retain)
	}

	po.OnConnect = func(cl paho3.Client) {
		// Restore subscriptions after (re)connects.
		for _, s := range c.subs.list() {
			cl.Subscribe(s.Filter, s.QoS, c.onMessage)
		}
		c.ch.sendEvent(Event{Kind: EventConnected, Detail: c.url})
		c.connOnce.Do(func() { close(c.connected) })
	}
	po.OnConnectionLost = func(_ paho3.Client, err error) {
		c.ch.sendEvent(Event{Kind: EventDisconnected, Err: err})
	}
	po.OnReconnecting = func(paho3.Client, *paho3.ClientOptions) {
		c.ch.sendEvent(Event{Kind: EventReconnecting})
	}

	c.client = paho3.NewClient(po)
	tok := c.client.Connect()
	if !tok.WaitTimeout(c.opts.ConnectTimeout) {
		c.client.Disconnect(0)
		return fmt.Errorf("connect to %s timed out after %s", c.url, c.opts.ConnectTimeout)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connect to %s: %w", c.url, err)
	}
	// Wait for OnConnect to finish: paho loses SUBSCRIBE packets sent while
	// connect finalisation is still in flight, so Subscribe must not be
	// callable before this point.
	select {
	case <-c.connected:
	case <-time.After(c.opts.ConnectTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}
	return ctx.Err()
}

func (c *v3Client) onMessage(_ paho3.Client, m paho3.Message) {
	c.ch.sendMsg(Message{
		Topic:     m.Topic(),
		Payload:   m.Payload(),
		QoS:       m.Qos(),
		Retained:  m.Retained(),
		Duplicate: m.Duplicate(),
		Time:      time.Now(),
	})
}

func (c *v3Client) Disconnect() {
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(250)
	}
	c.ch.close()
}

func (c *v3Client) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	tok := c.client.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("publish to %s timed out", topic)
	}
	return tok.Error()
}

func (c *v3Client) Subscribe(ctx context.Context, filter string, qos byte) error {
	tok := c.client.Subscribe(filter, qos, c.onMessage)
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("subscribe to %s timed out", filter)
	}
	if err := tok.Error(); err != nil {
		return err
	}
	c.subs.add(filter, qos)
	return nil
}

func (c *v3Client) Unsubscribe(ctx context.Context, filter string) error {
	tok := c.client.Unsubscribe(filter)
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("unsubscribe from %s timed out", filter)
	}
	if err := tok.Error(); err != nil {
		return err
	}
	c.subs.remove(filter)
	return nil
}

func (c *v3Client) Messages() <-chan Message      { return c.ch.msgs }
func (c *v3Client) Events() <-chan Event          { return c.ch.events }
func (c *v3Client) Subscriptions() []Subscription { return c.subs.list() }
func (c *v3Client) Dropped() uint64               { return c.ch.dropped.Load() }
func (c *v3Client) ServerURL() string             { return c.url }
func (c *v3Client) ProtocolVersion() Version      { return c.opts.Version }
