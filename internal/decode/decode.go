// Package decode auto-detects and renders MQTT payload formats: JSON,
// gzip-compressed JSON, Sparkplug B, OPC UA PubSub (JSON and UADP binary),
// plain text and hex.
package decode

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dscsystems/mqtt-console/internal/decode/opcua"
	"github.com/dscsystems/mqtt-console/internal/decode/sparkplug"
)

// Format identifies a payload rendering.
type Format int

const (
	FmtAuto Format = iota
	FmtJSON
	FmtText
	FmtHex
	FmtSparkplug
	FmtOPCUAUADP
	FmtGzip // gunzip, then auto-detect the inner payload
)

// Formats lists the user-selectable formats in cycling order.
var Formats = []Format{FmtAuto, FmtJSON, FmtText, FmtHex, FmtSparkplug, FmtOPCUAUADP, FmtGzip}

func (f Format) String() string {
	switch f {
	case FmtAuto:
		return "auto"
	case FmtJSON:
		return "json"
	case FmtText:
		return "text"
	case FmtHex:
		return "hex"
	case FmtSparkplug:
		return "sparkplug"
	case FmtOPCUAUADP:
		return "opcua-uadp"
	case FmtGzip:
		return "gzip"
	}
	return "unknown"
}

// ParseFormat maps a CLI string to a Format.
func ParseFormat(s string) (Format, error) {
	for _, f := range Formats {
		if f.String() == strings.ToLower(s) {
			return f, nil
		}
	}
	return FmtAuto, fmt.Errorf("unknown format %q (use auto, json, text, hex, sparkplug, opcua-uadp or gzip)", s)
}

// Result is a rendered payload.
type Result struct {
	Format string // human label, e.g. "JSON (gzip)" or "Sparkplug B"
	Text   string // rendered payload
	Err    error  // set when the requested format failed and Text is a fallback
}

const (
	maxGzipSize = 32 << 20 // decompression cap: gzip bombs stay bounded
	maxHexDump  = 8 << 10  // hex view cap
)

// Options carries decode context.
type Options struct {
	Topic string
	// SparkplugAliases resolves alias-only metrics in NDATA/DDATA messages;
	// learned from BIRTH messages (see store.Store).
	SparkplugAliases map[uint64]string
}

// Auto detects the payload format and renders it. Detection order: empty,
// gzip, Sparkplug (by topic namespace), JSON (with OPC UA JSON labelling),
// UADP (strict parse), UTF-8 text, hex.
func Auto(payload []byte, opts Options) Result {
	if len(payload) == 0 {
		return Result{Format: "Empty", Text: "(empty payload)"}
	}

	// gzip magic number.
	if len(payload) > 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		if inner, err := gunzip(payload); err == nil {
			r := Auto(inner, opts)
			r.Format += " (gzip)"
			return r
		}
	}

	// Sparkplug B, identified by its reserved topic namespace.
	if sparkplug.IsSparkplugTopic(opts.Topic) {
		if r, ok := trySparkplug(payload, opts); ok {
			return r
		}
	}

	// JSON (includes Sparkplug STATE messages and OPC UA JSON encoding).
	if looksLikeJSON(payload) && json.Valid(payload) {
		label := "JSON"
		if opcua.IsJSONNetworkMessage(payload) {
			label = "OPC UA PubSub (JSON)"
		}
		return Result{Format: label, Text: prettyJSON(payload)}
	}

	// UADP binary: the strict parser doubles as the detector.
	if nm, err := opcua.DecodeUADP(payload); err == nil {
		if out, err := marshal(nm); err == nil {
			return Result{Format: "OPC UA PubSub (UADP)", Text: out}
		}
	}

	if isMostlyText(payload) {
		return Result{Format: "Text", Text: string(payload)}
	}
	return Result{Format: "Hex", Text: hexDump(payload)}
}

// As renders the payload in a specific format, falling back to a hex view
// (with Err set) when the payload cannot be decoded as requested.
func As(f Format, payload []byte, opts Options) Result {
	if f == FmtAuto {
		return Auto(payload, opts)
	}
	if len(payload) == 0 {
		return Result{Format: "Empty", Text: "(empty payload)"}
	}
	switch f {
	case FmtJSON:
		if json.Valid(payload) {
			return Result{Format: "JSON", Text: prettyJSON(payload)}
		}
		return fallback("JSON", payload, fmt.Errorf("payload is not valid JSON"))
	case FmtText:
		if utf8.Valid(payload) {
			return Result{Format: "Text", Text: string(payload)}
		}
		return fallback("Text", payload, fmt.Errorf("payload is not valid UTF-8"))
	case FmtHex:
		return Result{Format: "Hex", Text: hexDump(payload)}
	case FmtSparkplug:
		if r, ok := trySparkplug(payload, opts); ok {
			return r
		}
		return fallback("Sparkplug B", payload, fmt.Errorf("payload is not a valid Sparkplug B protobuf"))
	case FmtOPCUAUADP:
		nm, err := opcua.DecodeUADP(payload)
		if err != nil {
			return fallback("OPC UA UADP", payload, err)
		}
		out, err := marshal(nm)
		if err != nil {
			return fallback("OPC UA UADP", payload, err)
		}
		return Result{Format: "OPC UA PubSub (UADP)", Text: out}
	case FmtGzip:
		inner, err := gunzip(payload)
		if err != nil {
			return fallback("gzip", payload, err)
		}
		r := Auto(inner, opts)
		r.Format += " (gzip)"
		return r
	}
	return Auto(payload, opts)
}

func fallback(want string, payload []byte, err error) Result {
	return Result{Format: want + " → Hex", Text: hexDump(payload), Err: err}
}

func gunzip(payload []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxGzipSize+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxGzipSize {
		return nil, fmt.Errorf("gzip payload exceeds %d MB decompressed", maxGzipSize>>20)
	}
	return out, nil
}

// sparkplugRender shapes a Sparkplug payload for JSON output with stable
// field order and human-readable types and timestamps.
type sparkplugRender struct {
	Topic     *sparkplugTopicRender `json:"topic,omitempty"`
	Timestamp string                `json:"timestamp,omitempty"`
	Seq       *uint64               `json:"seq,omitempty"`
	UUID      string                `json:"uuid,omitempty"`
	Metrics   []sparkplugMetric     `json:"metrics,omitempty"`
	BodyB64   []byte                `json:"body,omitempty"`
}

type sparkplugTopicRender struct {
	Group       string `json:"group,omitempty"`
	MessageType string `json:"message_type,omitempty"`
	EdgeNode    string `json:"edge_node,omitempty"`
	Device      string `json:"device,omitempty"`
}

type sparkplugMetric struct {
	Name         string         `json:"name,omitempty"`
	Alias        *uint64        `json:"alias,omitempty"`
	Type         string         `json:"type,omitempty"`
	Value        any            `json:"value"`
	Timestamp    string         `json:"timestamp,omitempty"`
	IsNull       bool           `json:"is_null,omitempty"`
	IsHistorical bool           `json:"is_historical,omitempty"`
	IsTransient  bool           `json:"is_transient,omitempty"`
	Properties   map[string]any `json:"properties,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func trySparkplug(payload []byte, opts Options) (Result, bool) {
	p, err := sparkplug.Decode(payload)
	if err != nil {
		return Result{}, false
	}
	sparkplug.ResolveAliases(p, opts.SparkplugAliases)

	out := sparkplugRender{Seq: p.Seq, UUID: p.UUID, BodyB64: p.Body}
	if p.Timestamp != nil {
		out.Timestamp = sparkplugTime(*p.Timestamp)
	}
	if ti, ok := sparkplug.ParseTopic(opts.Topic); ok {
		out.Topic = &sparkplugTopicRender{
			Group: ti.GroupID, MessageType: ti.MessageType,
			EdgeNode: ti.EdgeNodeID, Device: ti.DeviceID,
		}
	}
	for _, m := range p.Metrics {
		sm := sparkplugMetric{
			Name: m.Name, Alias: m.Alias, Value: m.Value,
			IsNull: m.IsNull, IsHistorical: m.IsHistorical, IsTransient: m.IsTransient,
			Properties: m.Properties, Metadata: m.Metadata,
		}
		if m.DataType != 0 {
			sm.Type = sparkplug.TypeName(m.DataType)
		}
		if m.Timestamp != nil {
			sm.Timestamp = sparkplugTime(*m.Timestamp)
		}
		out.Metrics = append(out.Metrics, sm)
	}
	text, err := marshal(out)
	if err != nil {
		return Result{}, false
	}
	return Result{Format: "Sparkplug B", Text: text}, true
}

func sparkplugTime(ms uint64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func looksLikeJSON(b []byte) bool {
	trimmed := bytes.TrimLeft(b, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{', '[', '"', 't', 'f', 'n', '-':
		return true
	default:
		return trimmed[0] >= '0' && trimmed[0] <= '9'
	}
}

func prettyJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(b), "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}

func marshal(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// isMostlyText accepts payloads that are valid UTF-8 with a high proportion
// of printable characters.
func isMostlyText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	printable, total := 0, 0
	for _, r := range string(b) {
		total++
		if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r != 0x7f && r != utf8.RuneError) {
			printable++
		}
	}
	return total > 0 && float64(printable)/float64(total) >= 0.9
}

func hexDump(b []byte) string {
	truncated := false
	if len(b) > maxHexDump {
		b = b[:maxHexDump]
		truncated = true
	}
	out := hex.Dump(b)
	if truncated {
		out += fmt.Sprintf("... (%d KiB shown)", maxHexDump>>10)
	}
	return out
}
