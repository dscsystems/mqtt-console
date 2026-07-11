// Package config loads and saves connection profiles from
// ~/.config/mqtt-console/config.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dscsystems/mqtt-console/internal/mqttc"
	"gopkg.in/yaml.v3"
)

// TLS mirrors mqttc.TLSOptions for YAML.
type TLS struct {
	CA       string `yaml:"ca,omitempty"`
	Cert     string `yaml:"cert,omitempty"`
	Key      string `yaml:"key,omitempty"`
	Insecure bool   `yaml:"insecure,omitempty"`
}

// Will mirrors mqttc.Will for YAML.
type Will struct {
	Topic   string `yaml:"topic"`
	Payload string `yaml:"payload,omitempty"`
	QoS     byte   `yaml:"qos,omitempty"`
	Retain  bool   `yaml:"retain,omitempty"`
}

// Connection is a saved broker profile. Username and password values support
// ${ENV_VAR} expansion so credentials can stay out of the file.
type Connection struct {
	Name       string   `yaml:"name"`
	URL        string   `yaml:"url"`
	Version    int      `yaml:"mqtt_version,omitempty"` // 3=3.1, 4=3.1.1, 5=5.0 (default 4)
	ClientID   string   `yaml:"client_id,omitempty"`
	Username   string   `yaml:"username,omitempty"`
	Password   string   `yaml:"password,omitempty"`
	KeepAlive  int      `yaml:"keepalive_seconds,omitempty"`
	CleanStart *bool    `yaml:"clean_start,omitempty"`
	TLS        TLS      `yaml:"tls,omitempty"`
	Topics     []string `yaml:"topics,omitempty"` // subscribed on connect
	Will       *Will    `yaml:"will,omitempty"`
}

// ToOptions converts a profile to client options, expanding ${ENV} references
// in the username and password.
func (c Connection) ToOptions() mqttc.Options {
	o := mqttc.Options{
		URL:      c.URL,
		ClientID: c.ClientID,
		Username: os.ExpandEnv(c.Username),
		Password: os.ExpandEnv(c.Password),
		Version:  mqttc.Version(c.Version),
		TLS: mqttc.TLSOptions{
			CAFile:   c.TLS.CA,
			CertFile: c.TLS.Cert,
			KeyFile:  c.TLS.Key,
			Insecure: c.TLS.Insecure,
		},
	}
	if c.Version == 0 {
		o.Version = mqttc.V311
	}
	if c.KeepAlive > 0 {
		o.KeepAlive = time.Duration(c.KeepAlive) * time.Second
	}
	o.CleanStart = true
	if c.CleanStart != nil {
		o.CleanStart = *c.CleanStart
	}
	if c.Will != nil {
		o.Will = &mqttc.Will{
			Topic:   c.Will.Topic,
			Payload: []byte(c.Will.Payload),
			QoS:     c.Will.QoS,
			Retain:  c.Will.Retain,
		}
	}
	return o
}

// Config is the persisted configuration.
type Config struct {
	Connections []Connection `yaml:"connections"`
	path        string
}

// Path returns the config file location.
func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mqtt-console", "config.yaml"), nil
}

// Load reads the config file; a missing file yields an empty config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{path: path}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config file with owner-only permissions (it may contain
// credentials).
func (c *Config) Save() error {
	if c.path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		c.path = p
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, raw, 0o600)
}

// Get returns the profile with the given name.
func (c *Config) Get(name string) (Connection, bool) {
	for _, conn := range c.Connections {
		if conn.Name == name {
			return conn, true
		}
	}
	return Connection{}, false
}

// Upsert inserts or replaces a profile by name.
func (c *Config) Upsert(conn Connection) {
	for i, existing := range c.Connections {
		if existing.Name == conn.Name {
			c.Connections[i] = conn
			return
		}
	}
	c.Connections = append(c.Connections, conn)
}

// Delete removes a profile by name.
func (c *Config) Delete(name string) {
	for i, conn := range c.Connections {
		if conn.Name == name {
			c.Connections = append(c.Connections[:i], c.Connections[i+1:]...)
			return
		}
	}
}
