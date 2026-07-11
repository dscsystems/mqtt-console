// Package cli implements the non-interactive pub and sub subcommands.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dscsystems/mqtt-console/internal/config"
	"github.com/dscsystems/mqtt-console/internal/decode"
	"github.com/dscsystems/mqtt-console/internal/mqttc"
)

// ConnFlags registers the shared connection flags on a FlagSet.
type ConnFlags struct {
	URL       string
	Profile   string
	Version   int
	ClientID  string
	Username  string
	Password  string
	CA        string
	Cert      string
	Key       string
	Insecure  bool
	KeepAlive int
}

func (c *ConnFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.URL, "url", "", "broker URL (tcp:// ssl:// mqtt:// mqtts:// ws:// wss://)")
	fs.StringVar(&c.URL, "u", "", "broker URL (shorthand)")
	fs.StringVar(&c.Profile, "profile", "", "saved connection profile name")
	fs.StringVar(&c.Profile, "c", "", "saved connection profile name (shorthand)")
	fs.IntVar(&c.Version, "V", 4, "MQTT version: 3 (3.1), 4 (3.1.1) or 5")
	fs.StringVar(&c.ClientID, "client-id", "", "client identifier (default: auto-generated)")
	fs.StringVar(&c.ClientID, "i", "", "client identifier (shorthand)")
	fs.StringVar(&c.Username, "username", "", "username")
	fs.StringVar(&c.Password, "password", "", "password")
	fs.StringVar(&c.CA, "ca", "", "CA certificate file (PEM)")
	fs.StringVar(&c.Cert, "cert", "", "client certificate file (PEM)")
	fs.StringVar(&c.Key, "key", "", "client private key file (PEM)")
	fs.BoolVar(&c.Insecure, "insecure", false, "skip TLS certificate verification")
	fs.IntVar(&c.KeepAlive, "keepalive", 30, "keep-alive interval in seconds")
}

// resolve merges flags with an optional saved profile into client options.
func (c *ConnFlags) resolve(fs *flag.FlagSet) (mqttc.Options, error) {
	var opts mqttc.Options
	if c.Profile != "" {
		cfg, err := config.Load()
		if err != nil {
			return opts, err
		}
		conn, ok := cfg.Get(c.Profile)
		if !ok {
			return opts, fmt.Errorf("no saved profile named %q", c.Profile)
		}
		opts = conn.ToOptions()
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if c.URL != "" {
		opts.URL = c.URL
	}
	if set["V"] || opts.Version == 0 {
		opts.Version = mqttc.Version(c.Version)
	}
	if c.ClientID != "" {
		opts.ClientID = c.ClientID
	}
	if c.Username != "" {
		opts.Username = c.Username
	}
	if c.Password != "" {
		opts.Password = c.Password
	}
	if c.CA != "" {
		opts.TLS.CAFile = c.CA
	}
	if c.Cert != "" {
		opts.TLS.CertFile = c.Cert
	}
	if c.Key != "" {
		opts.TLS.KeyFile = c.Key
	}
	if c.Insecure {
		opts.TLS.Insecure = true
	}
	if set["keepalive"] || opts.KeepAlive == 0 {
		opts.KeepAlive = time.Duration(c.KeepAlive) * time.Second
	}
	opts.CleanStart = true
	if opts.URL == "" {
		return opts, fmt.Errorf("a broker URL is required (--url or --profile)")
	}
	return opts, nil
}

func dial(opts mqttc.Options) (mqttc.Client, error) {
	client, err := mqttc.New(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// Pub implements `mqtt-console pub`.
func Pub(args []string) error {
	fs := flag.NewFlagSet("pub", flag.ExitOnError)
	conn := &ConnFlags{}
	conn.register(fs)
	topic := fs.String("t", "", "topic to publish to (required)")
	fs.StringVar(topic, "topic", "", "topic to publish to (required)")
	message := fs.String("m", "", "payload string")
	fs.StringVar(message, "message", "", "payload string")
	file := fs.String("f", "", "read payload from file")
	fs.StringVar(file, "file", "", "read payload from file")
	useStdin := fs.Bool("stdin", false, "read payload from stdin")
	qos := fs.Int("q", 0, "QoS level (0, 1 or 2)")
	fs.IntVar(qos, "qos", 0, "QoS level (0, 1 or 2)")
	retain := fs.Bool("r", false, "set the retain flag")
	fs.BoolVar(retain, "retain", false, "set the retain flag")
	fs.Usage = usageFor(fs, "mqtt-console pub -t TOPIC (-m MSG | -f FILE | --stdin) [connection flags]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *topic == "" {
		fs.Usage()
		return fmt.Errorf("a topic is required (-t)")
	}
	if *qos < 0 || *qos > 2 {
		return fmt.Errorf("QoS must be 0, 1 or 2")
	}

	var payload []byte
	switch {
	case *file != "":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		payload = data
	case *useStdin:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		payload = data
	default:
		payload = []byte(*message)
	}

	opts, err := conn.resolve(fs)
	if err != nil {
		return err
	}
	client, err := dial(opts)
	if err != nil {
		return err
	}
	defer client.Disconnect()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Publish(ctx, *topic, byte(*qos), *retain, payload); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "published %d bytes to %s (QoS %d, retain %v)\n",
		len(payload), *topic, *qos, *retain)
	return nil
}

// Sub implements `mqtt-console sub`.
func Sub(args []string) error {
	fs := flag.NewFlagSet("sub", flag.ExitOnError)
	conn := &ConnFlags{}
	conn.register(fs)
	var topics multiFlag
	fs.Var(&topics, "t", "topic filter to subscribe to (repeatable)")
	fs.Var(&topics, "topic", "topic filter to subscribe to (repeatable)")
	qos := fs.Int("q", 0, "QoS level (0, 1 or 2)")
	fs.IntVar(qos, "qos", 0, "QoS level (0, 1 or 2)")
	count := fs.Int("C", 0, "exit after receiving this many messages (0 = run until interrupted)")
	format := fs.String("format", "auto", "payload rendering: auto, json, text, hex, sparkplug, opcua-uadp, gzip, raw")
	timeout := fs.Duration("timeout", 0, "exit after this duration (e.g. 30s; 0 = no limit)")
	quiet := fs.Bool("quiet", false, "print payloads only, no topic/metadata header")
	fs.Usage = usageFor(fs, "mqtt-console sub -t FILTER [-t FILTER...] [connection flags]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(topics) == 0 {
		topics = []string{"#"}
	}
	if *qos < 0 || *qos > 2 {
		return fmt.Errorf("QoS must be 0, 1 or 2")
	}
	raw := *format == "raw"
	var fmtSel decode.Format
	if !raw {
		var err error
		fmtSel, err = decode.ParseFormat(*format)
		if err != nil {
			return err
		}
	}

	opts, err := conn.resolve(fs)
	if err != nil {
		return err
	}
	client, err := dial(opts)
	if err != nil {
		return err
	}
	defer client.Disconnect()

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, t := range topics {
		if err := client.Subscribe(ctx, t, byte(*qos)); err != nil {
			return fmt.Errorf("subscribing to %s: %w", t, err)
		}
		fmt.Fprintf(os.Stderr, "subscribed to %s (QoS %d)\n", t, *qos)
	}

	received := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-client.Messages():
			if !ok {
				return nil
			}
			printMessage(m, raw, fmtSel, *quiet)
			received++
			if *count > 0 && received >= *count {
				return nil
			}
		case e := <-client.Events():
			if e.Kind == mqttc.EventDisconnected && e.Err != nil {
				fmt.Fprintf(os.Stderr, "connection lost: %v (reconnecting)\n", e.Err)
			}
		}
	}
}

func printMessage(m mqttc.Message, raw bool, f decode.Format, quiet bool) {
	if raw {
		os.Stdout.Write(m.Payload)
		fmt.Println()
		return
	}
	res := decode.As(f, m.Payload, decode.Options{Topic: m.Topic})
	if quiet {
		fmt.Println(res.Text)
		return
	}
	flags := fmt.Sprintf("QoS %d", m.QoS)
	if m.Retained {
		flags += ", retained"
	}
	fmt.Printf("── %s  %s  (%s, %d bytes, %s)\n%s\n",
		m.Time.Format("15:04:05.000"), m.Topic, flags, len(m.Payload), res.Format, res.Text)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func usageFor(fs *flag.FlagSet, usage string) func() {
	return func() {
		fmt.Fprintf(os.Stderr, "Usage: %s\n\nFlags:\n", usage)
		fs.PrintDefaults()
	}
}
