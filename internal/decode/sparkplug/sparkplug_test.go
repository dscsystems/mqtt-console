package sparkplug

import (
	"math"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// buildMetric encodes a metric with a name, datatype and one value field.
func buildMetric(name string, alias uint64, datatype uint32, valueField protowire.Number, appendValue func([]byte) []byte) []byte {
	var m []byte
	if name != "" {
		m = protowire.AppendTag(m, 1, protowire.BytesType)
		m = protowire.AppendString(m, name)
	}
	if alias != 0 {
		m = protowire.AppendTag(m, 2, protowire.VarintType)
		m = protowire.AppendVarint(m, alias)
	}
	m = protowire.AppendTag(m, 4, protowire.VarintType)
	m = protowire.AppendVarint(m, uint64(datatype))
	if appendValue != nil {
		m = appendValue(m)
	}
	return m
}

func wrapPayload(timestamp, seq uint64, metrics ...[]byte) []byte {
	var p []byte
	p = protowire.AppendTag(p, 1, protowire.VarintType)
	p = protowire.AppendVarint(p, timestamp)
	for _, m := range metrics {
		p = protowire.AppendTag(p, 2, protowire.BytesType)
		p = protowire.AppendBytes(p, m)
	}
	p = protowire.AppendTag(p, 3, protowire.VarintType)
	p = protowire.AppendVarint(p, seq)
	return p
}

func TestDecodeBasicMetrics(t *testing.T) {
	temp := buildMetric("temperature", 0, TypeDouble, 13, func(b []byte) []byte {
		b = protowire.AppendTag(b, 13, protowire.Fixed64Type)
		return protowire.AppendFixed64(b, math.Float64bits(21.5))
	})
	on := buildMetric("running", 0, TypeBoolean, 14, func(b []byte) []byte {
		b = protowire.AppendTag(b, 14, protowire.VarintType)
		return protowire.AppendVarint(b, 1)
	})
	name := buildMetric("device/name", 0, TypeString, 15, func(b []byte) []byte {
		b = protowire.AppendTag(b, 15, protowire.BytesType)
		return protowire.AppendString(b, "pump-7")
	})
	neg := buildMetric("delta", 0, TypeInt32, 10, func(b []byte) []byte {
		b = protowire.AppendTag(b, 10, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(uint32(0xFFFFFFFF))) // -1 as two's complement
	})

	raw := wrapPayload(1700000000000, 3, temp, on, name, neg)
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.Timestamp == nil || *p.Timestamp != 1700000000000 {
		t.Errorf("timestamp = %v, want 1700000000000", p.Timestamp)
	}
	if p.Seq == nil || *p.Seq != 3 {
		t.Errorf("seq = %v, want 3", p.Seq)
	}
	if len(p.Metrics) != 4 {
		t.Fatalf("got %d metrics, want 4", len(p.Metrics))
	}
	if v, ok := p.Metrics[0].Value.(float64); !ok || v != 21.5 {
		t.Errorf("temperature = %v (%T), want 21.5", p.Metrics[0].Value, p.Metrics[0].Value)
	}
	if v, ok := p.Metrics[1].Value.(bool); !ok || !v {
		t.Errorf("running = %v, want true", p.Metrics[1].Value)
	}
	if v, ok := p.Metrics[2].Value.(string); !ok || v != "pump-7" {
		t.Errorf("device/name = %v, want pump-7", p.Metrics[2].Value)
	}
	if v, ok := p.Metrics[3].Value.(int32); !ok || v != -1 {
		t.Errorf("delta = %v (%T), want int32(-1)", p.Metrics[3].Value, p.Metrics[3].Value)
	}
}

func TestDecodeDateTime(t *testing.T) {
	dt := buildMetric("boot", 0, TypeDateTime, 11, func(b []byte) []byte {
		b = protowire.AppendTag(b, 11, protowire.VarintType)
		return protowire.AppendVarint(b, 1700000000000)
	})
	p, err := Decode(wrapPayload(1, 0, dt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := p.Metrics[0].Value; got != "2023-11-14T22:13:20.000Z" {
		t.Errorf("boot = %v, want 2023-11-14T22:13:20.000Z", got)
	}
}

func TestAliasResolution(t *testing.T) {
	birthMetric := buildMetric("engine/rpm", 7, TypeInt64, 11, func(b []byte) []byte {
		b = protowire.AppendTag(b, 11, protowire.VarintType)
		return protowire.AppendVarint(b, 900)
	})
	birth, err := Decode(wrapPayload(1, 0, birthMetric))
	if err != nil {
		t.Fatalf("Decode birth: %v", err)
	}
	aliases := HarvestAliases(birth)
	if aliases[7] != "engine/rpm" {
		t.Fatalf("aliases = %v, want 7 -> engine/rpm", aliases)
	}

	dataMetric := buildMetric("", 7, TypeInt64, 11, func(b []byte) []byte {
		b = protowire.AppendTag(b, 11, protowire.VarintType)
		return protowire.AppendVarint(b, 950)
	})
	data, err := Decode(wrapPayload(2, 1, dataMetric))
	if err != nil {
		t.Fatalf("Decode data: %v", err)
	}
	ResolveAliases(data, aliases)
	if data.Metrics[0].Name != "engine/rpm" {
		t.Errorf("resolved name = %q, want engine/rpm", data.Metrics[0].Name)
	}
	if v, _ := data.Metrics[0].Value.(int64); v != 950 {
		t.Errorf("value = %v, want 950", data.Metrics[0].Value)
	}
}

func TestDecodeIsNull(t *testing.T) {
	m := buildMetric("gone", 0, TypeDouble, 0, func(b []byte) []byte {
		b = protowire.AppendTag(b, 7, protowire.VarintType) // is_null
		return protowire.AppendVarint(b, 1)
	})
	p, err := Decode(wrapPayload(1, 0, m))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !p.Metrics[0].IsNull || p.Metrics[0].Value != nil {
		t.Errorf("expected null metric, got %+v", p.Metrics[0])
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Error("expected error decoding garbage")
	}
}

func TestParseTopic(t *testing.T) {
	tests := []struct {
		topic string
		ok    bool
		want  TopicInfo
	}{
		{"spBv1.0/plant1/NBIRTH/edge01", true, TopicInfo{GroupID: "plant1", MessageType: "NBIRTH", EdgeNodeID: "edge01"}},
		{"spBv1.0/plant1/DDATA/edge01/dev42", true, TopicInfo{GroupID: "plant1", MessageType: "DDATA", EdgeNodeID: "edge01", DeviceID: "dev42"}},
		{"spBv1.0/STATE/scada-host", true, TopicInfo{MessageType: "STATE", EdgeNodeID: "scada-host"}},
		{"sensors/temp", false, TopicInfo{}},
		{"spBv1.0/onlygroup", false, TopicInfo{}},
	}
	for _, tc := range tests {
		got, ok := ParseTopic(tc.topic)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseTopic(%q) = %+v, %v; want %+v, %v", tc.topic, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDecodeInt16Array(t *testing.T) {
	// Sparkplug 3.0 packed little-endian arrays in bytes_value.
	m := buildMetric("arr", 0, TypeInt16Array, 16, func(b []byte) []byte {
		b = protowire.AppendTag(b, 16, protowire.BytesType)
		return protowire.AppendBytes(b, []byte{0x01, 0x00, 0xff, 0xff}) // [1, -1]
	})
	p, err := Decode(wrapPayload(1, 0, m))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	arr, ok := p.Metrics[0].Value.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("value = %v (%T), want 2-element array", p.Metrics[0].Value, p.Metrics[0].Value)
	}
	if arr[0] != int16(1) || arr[1] != int16(-1) {
		t.Errorf("array = %v, want [1 -1]", arr)
	}
}
