package decode

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAutoJSON(t *testing.T) {
	r := Auto([]byte(`{"temp": 21.5, "unit": "C"}`), Options{Topic: "sensors/1"})
	if r.Format != "JSON" {
		t.Errorf("format = %q, want JSON", r.Format)
	}
	if !strings.Contains(r.Text, `"temp": 21.5`) {
		t.Errorf("pretty output missing field: %s", r.Text)
	}
}

func TestAutoGzipJSON(t *testing.T) {
	payload := gz(t, []byte(`{"a":1}`))
	r := Auto(payload, Options{})
	if r.Format != "JSON (gzip)" {
		t.Errorf("format = %q, want JSON (gzip)", r.Format)
	}
	if !strings.Contains(r.Text, `"a": 1`) {
		t.Errorf("unexpected text: %s", r.Text)
	}
}

func TestAutoText(t *testing.T) {
	r := Auto([]byte("hello world"), Options{})
	if r.Format != "Text" || r.Text != "hello world" {
		t.Errorf("got %q/%q", r.Format, r.Text)
	}
}

func TestAutoHexForBinary(t *testing.T) {
	r := Auto([]byte{0x00, 0x01, 0x02, 0xfe, 0xff}, Options{})
	if r.Format != "Hex" {
		t.Errorf("format = %q, want Hex", r.Format)
	}
}

func TestAutoEmpty(t *testing.T) {
	r := Auto(nil, Options{})
	if r.Format != "Empty" {
		t.Errorf("format = %q, want Empty", r.Format)
	}
}

func TestAutoSparkplug(t *testing.T) {
	var m []byte
	m = protowire.AppendTag(m, 1, protowire.BytesType)
	m = protowire.AppendString(m, "speed")
	m = protowire.AppendTag(m, 4, protowire.VarintType)
	m = protowire.AppendVarint(m, 10) // Double
	m = protowire.AppendTag(m, 13, protowire.Fixed64Type)
	m = protowire.AppendFixed64(m, 0x4045000000000000) // 42.0

	var p []byte
	p = protowire.AppendTag(p, 1, protowire.VarintType)
	p = protowire.AppendVarint(p, 1700000000000)
	p = protowire.AppendTag(p, 2, protowire.BytesType)
	p = protowire.AppendBytes(p, m)

	r := Auto(p, Options{Topic: "spBv1.0/g1/NDATA/edge1"})
	if r.Format != "Sparkplug B" {
		t.Fatalf("format = %q, want Sparkplug B", r.Format)
	}
	if !strings.Contains(r.Text, `"speed"`) || !strings.Contains(r.Text, "42") {
		t.Errorf("text missing metric: %s", r.Text)
	}
	if !strings.Contains(r.Text, `"message_type": "NDATA"`) {
		t.Errorf("text missing topic info: %s", r.Text)
	}
}

func TestAutoOPCUAJSON(t *testing.T) {
	payload := []byte(`{"MessageId":"m1","MessageType":"ua-data","PublisherId":"pub","Messages":[]}`)
	r := Auto(payload, Options{})
	if r.Format != "OPC UA PubSub (JSON)" {
		t.Errorf("format = %q, want OPC UA PubSub (JSON)", r.Format)
	}
}

func TestAsForcedFormatFallback(t *testing.T) {
	r := As(FmtJSON, []byte{0xde, 0xad}, Options{})
	if r.Err == nil {
		t.Error("expected decode error for forced JSON on binary")
	}
	if !strings.Contains(r.Format, "Hex") {
		t.Errorf("fallback format = %q, want hex fallback", r.Format)
	}
}

func TestAsGzip(t *testing.T) {
	payload := gz(t, []byte("plain text inside"))
	r := As(FmtGzip, payload, Options{})
	if r.Format != "Text (gzip)" {
		t.Errorf("format = %q, want Text (gzip)", r.Format)
	}
}

func TestParseFormat(t *testing.T) {
	for _, f := range Formats {
		got, err := ParseFormat(f.String())
		if err != nil || got != f {
			t.Errorf("ParseFormat(%q) = %v, %v", f.String(), got, err)
		}
	}
	if _, err := ParseFormat("nope"); err == nil {
		t.Error("expected error for unknown format")
	}
}
