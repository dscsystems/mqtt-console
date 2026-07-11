package opcua

import (
	"encoding/binary"
	"math"
	"testing"
)

// buildUADP constructs a minimal valid UADP network message: version 1,
// publisher id (uint16), payload header with one dataset writer, one
// key-frame dataset message with variant-encoded fields.
func buildUADP(t *testing.T, fields ...func([]byte) []byte) []byte {
	t.Helper()
	var b []byte
	b = append(b, 0x01|0x10|0x40|0x80)            // version 1, publisher id, payload header, extended flags 1
	b = append(b, 0x01)                           // ext1: publisher id type = uint16
	b = binary.LittleEndian.AppendUint16(b, 4711) // publisher id
	b = append(b, 0x01)                           // payload header: 1 dataset message
	b = binary.LittleEndian.AppendUint16(b, 99)   // dataset writer id

	// DataSetMessage: valid, variant encoding, no flags2.
	b = append(b, 0x01)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(fields))) // field count
	for _, f := range fields {
		b = f(b)
	}
	return b
}

func varDouble(v float64) func([]byte) []byte {
	return func(b []byte) []byte {
		b = append(b, 11) // Double
		return binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
	}
}

func varString(s string) func([]byte) []byte {
	return func(b []byte) []byte {
		b = append(b, 12) // String
		b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
		return append(b, s...)
	}
}

func varBool(v bool) func([]byte) []byte {
	return func(b []byte) []byte {
		b = append(b, 1) // Boolean
		if v {
			return append(b, 1)
		}
		return append(b, 0)
	}
}

func TestDecodeUADPKeyFrame(t *testing.T) {
	raw := buildUADP(t, varDouble(3.5), varString("ok"), varBool(true))
	nm, err := DecodeUADP(raw)
	if err != nil {
		t.Fatalf("DecodeUADP: %v", err)
	}
	if nm.PublisherID != uint16(4711) {
		t.Errorf("publisher id = %v, want 4711", nm.PublisherID)
	}
	if len(nm.DataSetMessages) != 1 {
		t.Fatalf("got %d dataset messages, want 1", len(nm.DataSetMessages))
	}
	dsm := nm.DataSetMessages[0]
	if dsm.WriterID == nil || *dsm.WriterID != 99 {
		t.Errorf("writer id = %v, want 99", dsm.WriterID)
	}
	if dsm.MessageType != "KeyFrame" || dsm.FieldEncoding != "Variant" {
		t.Errorf("type/encoding = %s/%s, want KeyFrame/Variant", dsm.MessageType, dsm.FieldEncoding)
	}
	if len(dsm.Fields) != 3 {
		t.Fatalf("got %d fields, want 3", len(dsm.Fields))
	}
	if v, ok := dsm.Fields[0].(float64); !ok || v != 3.5 {
		t.Errorf("field 0 = %v, want 3.5", dsm.Fields[0])
	}
	if v, ok := dsm.Fields[1].(string); !ok || v != "ok" {
		t.Errorf("field 1 = %v, want ok", dsm.Fields[1])
	}
	if v, ok := dsm.Fields[2].(bool); !ok || !v {
		t.Errorf("field 2 = %v, want true", dsm.Fields[2])
	}
}

func TestDecodeUADPRejectsGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		{0x00},
		{0x05, 0x01, 0x02},              // version 5
		[]byte("hello world, not uadp"), // text
		{0xff, 0xff, 0xff, 0xff, 0xff},  // version 15 + noise
	}
	for _, c := range cases {
		if _, err := DecodeUADP(c); err == nil {
			t.Errorf("expected error for %v", c)
		}
	}
}

func TestIsJSONNetworkMessage(t *testing.T) {
	yes := [][]byte{
		[]byte(`{"MessageId":"1","MessageType":"ua-data","Messages":[]}`),
		[]byte(`{"MessageId":"9","PublisherId":"p","Messages":[{"Payload":{}}]}`),
	}
	no := [][]byte{
		[]byte(`{"value":42}`),
		[]byte(`[1,2,3]`),
		[]byte(`not json`),
	}
	for _, b := range yes {
		if !IsJSONNetworkMessage(b) {
			t.Errorf("expected true for %s", b)
		}
	}
	for _, b := range no {
		if IsJSONNetworkMessage(b) {
			t.Errorf("expected false for %s", b)
		}
	}
}
