// mqtt-console is a terminal MQTT explorer: a Bubble Tea TUI for browsing
// topics with payload decoding (JSON, gzipped JSON, Sparkplug B, OPC UA
// PubSub), plus one-shot pub/sub subcommands.

/*
 * © 2026-Present Ricardo L. Olsen / DSC Systems ALL RIGHTS RESERVED
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful, but
 * WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
 * General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program. If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dscsystems/mqtt-console/internal/cli"
	"github.com/dscsystems/mqtt-console/internal/config"
	"github.com/dscsystems/mqtt-console/internal/ui"
)

var version = "0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "pub":
			exitOn(cli.Pub(args[1:]))
			return
		case "sub":
			exitOn(cli.Sub(args[1:]))
			return
		case "version", "--version", "-v":
			fmt.Printf("mqtt-console %s\n", version)
			return
		case "help", "--help", "-h":
			usage()
			return
		}
	}
	exitOn(runTUI(args))
}

func usage() {
	fmt.Print(`mqtt-console - terminal MQTT explorer

Usage:
  mqtt-console [flags]             open the interactive explorer
  mqtt-console pub [flags]         publish a single message
  mqtt-console sub [flags]         subscribe and print messages
  mqtt-console version             print the version

Explorer flags:
  -c, --profile NAME   connect immediately using a saved profile
  -u, --url URL        connect immediately to this broker
  -V N                 MQTT version: 3 (3.1), 4 (3.1.1) or 5 (default 4)
  -i, --client-id ID   client identifier
      --username USER  username
      --password PASS  password
      --ca FILE        CA certificate (PEM)
      --cert FILE      client certificate (PEM)
      --key FILE       client private key (PEM)
      --insecure       skip TLS certificate verification
  -t, --topic FILTER   initial subscription (repeatable, default "#")

Run "mqtt-console pub -h" or "mqtt-console sub -h" for subcommand flags.
Profiles are stored in ` + configPathHint() + `
`)
}

func configPathHint() string {
	p, err := config.Path()
	if err != nil {
		return "~/.config/mqtt-console/config.yaml"
	}
	return p
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("mqtt-console", flag.ExitOnError)
	profile := fs.String("profile", "", "saved profile to connect to")
	fs.StringVar(profile, "c", "", "saved profile to connect to (shorthand)")
	url := fs.String("url", "", "broker URL to connect to")
	fs.StringVar(url, "u", "", "broker URL (shorthand)")
	ver := fs.Int("V", 4, "MQTT version: 3, 4 or 5")
	clientID := fs.String("client-id", "", "client identifier")
	fs.StringVar(clientID, "i", "", "client identifier (shorthand)")
	username := fs.String("username", "", "username")
	password := fs.String("password", "", "password")
	ca := fs.String("ca", "", "CA certificate file")
	cert := fs.String("cert", "", "client certificate file")
	key := fs.String("key", "", "client private key file")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification")
	var topics topicList
	fs.Var(&topics, "t", "initial subscription (repeatable)")
	fs.Var(&topics, "topic", "initial subscription (repeatable)")
	fs.Usage = usage
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	var auto *config.Connection
	switch {
	case *profile != "":
		conn, ok := cfg.Get(*profile)
		if !ok {
			return fmt.Errorf("no saved profile named %q", *profile)
		}
		if len(topics) > 0 {
			conn.Topics = topics
		}
		auto = &conn
	case *url != "":
		auto = &config.Connection{
			URL:      *url,
			Version:  *ver,
			ClientID: *clientID,
			Username: *username,
			Password: *password,
			TLS:      config.TLS{CA: *ca, Cert: *cert, Key: *key, Insecure: *insecure},
			Topics:   topics,
		}
	}

	p := tea.NewProgram(ui.NewApp(cfg, auto), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type topicList []string

func (t *topicList) String() string     { return strings.Join(*t, ",") }
func (t *topicList) Set(v string) error { *t = append(*t, v); return nil }
