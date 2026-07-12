package mqttc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// v5Client wraps eclipse/paho.golang (autopaho) for MQTT 5.0.
type v5Client struct {
	opts   Options
	cm     *autopaho.ConnectionManager
	subs   *subTracker
	ch     *chanPair
	url    string
	cancel context.CancelFunc
}

var v5SchemeAliases = map[string]string{
	"tcp": "mqtt",
	"ssl": "mqtts",
	"tls": "mqtts",
}

func newV5(o Options) *v5Client {
	return &v5Client{opts: o, subs: newSubTracker(), ch: newChanPair()}
}

func (c *v5Client) Connect(ctx context.Context) error {
	u, err := normalizeURL(c.opts.URL, v5SchemeAliases)
	if err != nil {
		return err
	}
	c.url = u.String()

	tlsCfg, err := buildTLSConfig(c.opts, u.Scheme)
	if err != nil {
		return err
	}

	// The connection manager lives beyond Connect(); it owns reconnects.
	connCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		TlsCfg:                        tlsCfg,
		KeepAlive:                     uint16(c.opts.KeepAlive / time.Second),
		CleanStartOnInitialConnection: c.opts.CleanStart,
		SessionExpiryInterval:         60,
		ConnectTimeout:                c.opts.ConnectTimeout,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			for _, s := range c.subs.list() {
				_, _ = cm.Subscribe(connCtx, &paho.Subscribe{
					Subscriptions: []paho.SubscribeOptions{{Topic: s.Filter, QoS: s.QoS}},
				})
			}
			c.ch.sendEvent(Event{Kind: EventConnected, Detail: c.url})
		},
		OnConnectError: func(err error) {
			c.ch.sendEvent(Event{Kind: EventReconnecting, Err: err})
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.opts.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					c.onMessage(pr.Packet)
					return true, nil
				},
			},
			OnClientError: func(err error) {
				c.ch.sendEvent(Event{Kind: EventDisconnected, Err: err})
			},
			OnServerDisconnect: func(d *paho.Disconnect) {
				detail := ""
				if d.Properties != nil {
					detail = d.Properties.ReasonString
				}
				c.ch.sendEvent(Event{
					Kind:   EventDisconnected,
					Err:    fmt.Errorf("server disconnect (reason code %d)", d.ReasonCode),
					Detail: detail,
				})
			},
		},
	}
	if c.opts.Username != "" {
		cfg.ConnectUsername = c.opts.Username
	}
	if c.opts.Password != "" {
		cfg.ConnectPassword = []byte(c.opts.Password)
	}
	if w := c.opts.Will; w != nil {
		cfg.WillMessage = &paho.WillMessage{
			Topic:   w.Topic,
			Payload: w.Payload,
			QoS:     w.QoS,
			Retain:  w.Retain,
		}
	}

	cm, err := autopaho.NewConnection(connCtx, cfg)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to %s: %w", c.url, err)
	}
	c.cm = cm

	waitCtx, waitCancel := context.WithTimeout(ctx, c.opts.ConnectTimeout)
	defer waitCancel()
	if err := cm.AwaitConnection(waitCtx); err != nil {
		cancel()
		return fmt.Errorf("connect to %s: %w", c.url, err)
	}
	return nil
}

func (c *v5Client) onMessage(p *paho.Publish) {
	m := Message{
		Topic:    p.Topic,
		Payload:  p.Payload,
		QoS:      p.QoS,
		Retained: p.Retain,
		Time:     time.Now(),
	}
	if p.Properties != nil {
		m.ContentType = p.Properties.ContentType
		for _, up := range p.Properties.User {
			m.UserProps = append(m.UserProps, [2]string{up.Key, up.Value})
		}
	}
	c.ch.sendMsg(m)
}

func (c *v5Client) Disconnect() {
	if c.cm != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.cm.Disconnect(ctx)
		cancel()
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.ch.close()
}

func (c *v5Client) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	_, err := c.cm.Publish(ctx, &paho.Publish{
		Topic:   topic,
		QoS:     qos,
		Retain:  retain,
		Payload: payload,
	})
	return err
}

// Subscribe sends SUBSCRIBE and waits for the SUBACK. A connection drop in
// that window leaves the request hanging (paho.golang does not fail or
// re-send it after autopaho reconnects), so each attempt gets its own
// timeout and connection-state failures are retried. A retry can duplicate
// a SUBSCRIBE the broker already processed; that is idempotent.
func (c *v5Client) Subscribe(ctx context.Context, filter string, qos byte) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			awaitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			werr := c.cm.AwaitConnection(awaitCtx)
			cancel()
			if werr != nil {
				return err
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = c.cm.Subscribe(attemptCtx, &paho.Subscribe{
			Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: qos}},
		})
		cancel()
		if err == nil {
			c.subs.add(filter, qos)
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if !errors.Is(err, autopaho.ConnectionDownError) &&
			!errors.Is(err, paho.ErrConnectionLost) &&
			!errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return err
}

func (c *v5Client) Unsubscribe(ctx context.Context, filter string) error {
	_, err := c.cm.Unsubscribe(ctx, &paho.Unsubscribe{Topics: []string{filter}})
	if err != nil {
		return err
	}
	c.subs.remove(filter)
	return nil
}

func (c *v5Client) Messages() <-chan Message      { return c.ch.msgs }
func (c *v5Client) Events() <-chan Event          { return c.ch.events }
func (c *v5Client) Subscriptions() []Subscription { return c.subs.list() }
func (c *v5Client) Dropped() uint64               { return c.ch.dropped.Load() }
func (c *v5Client) ServerURL() string             { return c.url }
func (c *v5Client) ProtocolVersion() Version      { return V5 }
