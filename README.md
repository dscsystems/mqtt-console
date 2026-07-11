# MQTT-Console

A terminal MQTT explorer written in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea). Browse a live topic tree, inspect and publish messages, and decode payloads automatically: JSON, gzipped JSON, Sparkplug B and OPC UA PubSub.

```
╭ MQTT Console  plant-broker  ssl://broker:8883 · MQTT 5.0  ● connected
│ msgs 12840 (214.0/s) · topics 312 · 4.1 MiB · subs [#]
╭──────────────────────────╮╭──────────────────────────────────────────╮
│ ▾ spBv1.0                ││ spBv1.0/plant1/NDATA/edge01              │
│   ▾ plant1               ││ 2026-07-10 16:22:02.275 · 18 B · QoS 0   │
│     ▾ NDATA              ││ format Sparkplug B (auto) · message 1/34 │
│       edge01 (34)        ││ {                                        │
│ ▾ home                   ││   "metrics": [                           │
│   ▾ kitchen              ││     { "name": "rpm", "value": 950 }      │
│     temp = {"t":21} (6)  ││   ...                                    │
╰──────────────────────────╯╰──────────────────────────────────────────╯
```

## Features

- **All common connection types**: MQTT 3.1, 3.1.1 and 5.0, over TCP, TLS, WebSocket and secure WebSocket (`tcp://`, `ssl://`, `mqtt://`, `mqtts://`, `ws://`, `wss://`), with username/password, custom CA, mutual TLS client certificates, last will, keep-alive and automatic reconnect with resubscribe.
- **Live topic tree** with per-branch topic/message counts, payload previews, retained badges, expand/collapse, and case-insensitive filtering.
- **Full mouse support**: click to select and expand topics, right-click to publish, wheel scrolling in every pane, clickable profiles, buttons, toggles and form fields.
- **Payload decoding**, automatic or forced per topic:
  - JSON (pretty-printed)
  - gzip-compressed payloads (decompressed, then auto-detected; JSON, Sparkplug etc.)
  - **Sparkplug B** (all metric types incl. datasets, templates, properties and packed arrays; timestamps rendered as ISO 8601; metric **aliases resolved automatically** from NBIRTH/DBIRTH messages)
  - **OPC UA PubSub**: JSON network messages (detected and labelled) and best-effort UADP binary decoding (variant and DataValue field encodings; RawData shown as hex since it needs the publisher's metadata)
  - plain text and hex dump fallback
- **Per-topic message history** (last 100 messages, navigable), message rate, byte counters, MQTT 5 user properties and content type display.
- **Publish** with QoS 0-2, retain flag and `@file` payloads; **clear retained** messages; subscribe/unsubscribe at runtime.
- **Connection profiles** saved to `~/.config/mqtt-console/config.yaml` (credentials support `${ENV_VAR}` expansion).
- **One-shot CLI**: `pub` and `sub` subcommands in the spirit of `mosquitto_pub`/`mosquitto_sub`, with the same decoders available for output.
- Pause capture, prune subtrees from the view, and export payloads to file in the format shown in the UI (`.json`/`.txt` when validly decoded, raw `.bin` otherwise; force the hex view to always export raw bytes).

## Install

```sh
go build -o mqtt-console .
# or
go install github.com/dscsystems/mqtt-console@latest
```

## Usage

### Interactive explorer

```sh
mqtt-console                          # profile manager
mqtt-console -u tcp://localhost:1883  # connect directly, subscribe to #
mqtt-console -c plant-broker         # connect using a saved profile
mqtt-console -u mqtts://broker:8883 -V 5 --username ops --password '${MQTT_PASS}' -t 'spBv1.0/#'
```

Key bindings (press `?` in the app for the full list):

| Key | Action |
| --- | --- |
| `↑↓` / `jk`, `enter` | navigate, expand/collapse |
| `tab` | switch focus between tree and payload pane |
| `/` | filter topics |
| `p` | publish (topic prefilled from selection) |
| `s` / `S` | subscribe / list and unsubscribe |
| `f` / `F` | cycle payload format (auto → json → text → hex → sparkplug → opcua-uadp → gzip) / reset |
| `[` / `]` | older / newer message on the selected topic |
| `e` | export the payload as shown: decoded views as `.json`, text as `.txt`, binary as raw `.bin` |
| `R` | clear a retained message (publishes empty retained, with confirmation) |
| `x` | remove a subtree from the view |
| `z` | pause / resume capture |
| `E` / `C` | expand all / collapse all |
| `q`, `ctrl+c` | back to profiles / quit |

The UI is fully mouse-aware:

| Mouse | Action |
| --- | --- |
| click | select a topic (or profile, form field, button); clicking the selected topic again expands/collapses it |
| right-click | open the publish form for that topic (or edit a profile on the connect screen) |
| wheel | scroll the tree or the payload pane under the pointer; moves the selection in lists and forms |
| click on toggles/choices | flips checkboxes and cycles choice fields (QoS, MQTT version) directly |

### One-shot publish

```sh
mqtt-console pub -u tcp://localhost:1883 -t sensors/temp -m '{"v":21.5}' -q 1
mqtt-console pub -c plant-broker -t cmd/restart -f payload.bin -r
echo '{"v":1}' | mqtt-console pub -u tcp://localhost:1883 -t sensors/temp --stdin
```

### One-shot subscribe

```sh
mqtt-console sub -u tcp://localhost:1883 -t 'sensors/#'
mqtt-console sub -c plant-broker -t 'spBv1.0/#' --format sparkplug
mqtt-console sub -u tcp://localhost:1883 -t '#' -C 10 --timeout 30s   # 10 messages or 30 s
mqtt-console sub -u tcp://localhost:1883 -t data/raw --format raw > dump.bin
```

Shared connection flags for all modes: `-u/--url`, `-c/--profile`, `-V` (3, 4 or 5), `-i/--client-id`, `--username`, `--password`, `--ca`, `--cert`, `--key`, `--insecure`, `--keepalive`.

## Configuration

`~/.config/mqtt-console/config.yaml`:

```yaml
connections:
  - name: plant-broker
    url: mqtts://broker.example.com:8883
    mqtt_version: 5            # 3 = 3.1, 4 = 3.1.1 (default), 5 = 5.0
    client_id: ops-console
    username: ops
    password: ${MQTT_PASS}     # expanded from the environment at connect time
    keepalive_seconds: 30
    clean_start: true
    tls:
      ca: /etc/ssl/plant-ca.pem
      cert: /etc/ssl/ops.pem   # optional mutual TLS
      key: /etc/ssl/ops.key
      insecure: false
    topics: ["spBv1.0/#", "plant/#"]   # subscribed on connect (default "#")
    will:
      topic: clients/ops-console/status
      payload: offline
      qos: 1
      retain: true
```

The file is written with `0600` permissions. Prefer `${ENV_VAR}` references over storing credentials in plain text.

## Decoding notes

- **Sparkplug B** is decoded directly from the protobuf wire format (no generated code). NDATA/DDATA metrics that carry only an alias are named using the alias table learned from the corresponding NBIRTH/DBIRTH, scoped per group/edge-node/device.
- **OPC UA UADP** decoding is best effort. It handles the network message header, group and payload headers, and key/delta-frame dataset messages in Variant or DataValue field encoding. RawData-encoded fields require the publisher's `DataSetMetaData` (not available on the wire) and are shown as hex. Encrypted network messages are labelled as such.
- Auto-detection order: gzip → Sparkplug (by `spBv1.0/` namespace) → JSON (OPC UA JSON labelled) → UADP (strict parse) → text → hex. When auto-detection guesses wrong, force a format for the topic with `f`.
- Gzip decompression is capped at 32 MiB to keep decompression bombs bounded.

## Development

```sh
go test ./...
go vet ./...
```

Package layout:

- `internal/mqttc` - version-agnostic client over `eclipse/paho.mqtt.golang` (3.1/3.1.1) and `eclipse/paho.golang` (5.0)
- `internal/decode` - format detection and rendering; `decode/sparkplug` and `decode/opcua` decoders
- `internal/store` - live topic tree, history, rates, Sparkplug alias learning
- `internal/ui` - Bubble Tea models (profiles, browser, forms, modals)
- `internal/cli` - `pub` and `sub` subcommands

## License

GPLv3.

## Copyright

© 2026-Present Ricardo L. Olsen / DSC Systems ALL RIGHTS RESERVED