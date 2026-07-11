// Package opcua decodes OPC UA PubSub network messages carried over MQTT:
// the JSON encoding (detected by its well-known fields) and a best-effort
// decoder for the UADP binary encoding (OPC UA Part 14).
package opcua

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// IsJSONNetworkMessage reports whether an already-parsed JSON object looks
// like an OPC UA PubSub JSON network message (OPC UA Part 14, 7.2.3).
func IsJSONNetworkMessage(b []byte) bool {
	var probe struct {
		MessageID   *string         `json:"MessageId"`
		MessageType *string         `json:"MessageType"`
		Messages    json.RawMessage `json:"Messages"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	if probe.MessageType != nil {
		switch *probe.MessageType {
		case "ua-data", "ua-metadata", "ua-keyframe", "ua-deltaframe", "ua-event", "ua-keepalive":
			return true
		}
	}
	return probe.MessageID != nil && probe.Messages != nil
}

// NetworkMessage is a decoded UADP network message, shaped for JSON rendering.
type NetworkMessage struct {
	Version           int               `json:"uadp_version"`
	PublisherID       any               `json:"publisher_id,omitempty"`
	DataSetClassID    string            `json:"dataset_class_id,omitempty"`
	Timestamp         string            `json:"timestamp,omitempty"`
	GroupHeader       *GroupHeader      `json:"group_header,omitempty"`
	SecurityEnabled   bool              `json:"security_enabled,omitempty"`
	PromotedFields    []any             `json:"promoted_fields,omitempty"`
	DataSetMessages   []*DataSetMessage `json:"dataset_messages,omitempty"`
	UndecodedTrailing string            `json:"undecoded_trailing_hex,omitempty"`
}

// GroupHeader is the optional UADP group header.
type GroupHeader struct {
	WriterGroupID        *uint16 `json:"writer_group_id,omitempty"`
	GroupVersion         *uint32 `json:"group_version,omitempty"`
	NetworkMessageNumber *uint16 `json:"network_message_number,omitempty"`
	SequenceNumber       *uint16 `json:"sequence_number,omitempty"`
}

// DataSetMessage is one decoded UADP dataset message.
type DataSetMessage struct {
	WriterID       *uint16 `json:"dataset_writer_id,omitempty"`
	Valid          bool    `json:"valid"`
	MessageType    string  `json:"message_type"`
	FieldEncoding  string  `json:"field_encoding"`
	SequenceNumber *uint16 `json:"sequence_number,omitempty"`
	Timestamp      string  `json:"timestamp,omitempty"`
	Status         *uint16 `json:"status,omitempty"`
	MajorVersion   *uint32 `json:"config_major_version,omitempty"`
	MinorVersion   *uint32 `json:"config_minor_version,omitempty"`
	Fields         []any   `json:"fields,omitempty"`
	RawDataHex     string  `json:"raw_data_hex,omitempty"`
	Note           string  `json:"note,omitempty"`
}

// DecodeUADP parses a UADP binary network message. It is strict about header
// structure so it can double as a format detector: random or non-UADP bytes
// should fail rather than produce garbage.
func DecodeUADP(b []byte) (*NetworkMessage, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("uadp: too short")
	}
	r := &reader{b: b}
	nm := &NetworkMessage{}

	flags := r.u8()
	version := int(flags & 0x0f)
	if version != 1 {
		return nil, fmt.Errorf("uadp: unsupported version %d", version)
	}
	nm.Version = version
	pubIDEnabled := flags&0x10 != 0
	groupHeaderEnabled := flags&0x20 != 0
	payloadHeaderEnabled := flags&0x40 != 0

	var ext1, ext2 byte
	if flags&0x80 != 0 {
		ext1 = r.u8()
	}
	if ext1&0x80 != 0 {
		ext2 = r.u8()
	}
	msgType := (ext2 >> 2) & 0x07
	if msgType != 0 {
		return nil, fmt.Errorf("uadp: unsupported network message type %d (only DataSetMessage payload supported)", msgType)
	}
	if ext2&0x01 != 0 {
		return nil, fmt.Errorf("uadp: chunked messages not supported")
	}

	if pubIDEnabled {
		switch ext1 & 0x07 {
		case 0:
			nm.PublisherID = r.u8()
		case 1:
			nm.PublisherID = r.u16()
		case 2:
			nm.PublisherID = r.u32()
		case 3:
			nm.PublisherID = r.u64()
		case 4:
			nm.PublisherID = r.str()
		default:
			return nil, fmt.Errorf("uadp: reserved publisher id type")
		}
	}
	if ext1&0x08 != 0 { // DataSetClassId
		nm.DataSetClassID = r.guid()
	}

	if groupHeaderEnabled {
		gh := &GroupHeader{}
		gf := r.u8()
		if gf&0x01 != 0 {
			v := r.u16()
			gh.WriterGroupID = &v
		}
		if gf&0x02 != 0 {
			v := r.u32()
			gh.GroupVersion = &v
		}
		if gf&0x04 != 0 {
			v := r.u16()
			gh.NetworkMessageNumber = &v
		}
		if gf&0x08 != 0 {
			v := r.u16()
			gh.SequenceNumber = &v
		}
		nm.GroupHeader = gh
	}

	var writerIDs []uint16
	if payloadHeaderEnabled {
		count := int(r.u8())
		if count == 0 || count > 128 {
			return nil, fmt.Errorf("uadp: implausible dataset message count %d", count)
		}
		for i := 0; i < count; i++ {
			writerIDs = append(writerIDs, r.u16())
		}
	}

	if ext1&0x20 != 0 { // Timestamp
		nm.Timestamp = fmtDateTime(r.dateTime())
	}
	if ext1&0x40 != 0 { // PicoSeconds
		r.u16()
	}
	if ext2&0x02 != 0 { // PromotedFields
		size := int(r.u16())
		r.skip(size)
		nm.PromotedFields = []any{fmt.Sprintf("(%d bytes, not decoded)", size)}
	}
	if ext1&0x10 != 0 { // SecurityHeader
		nm.SecurityEnabled = true
		sf := r.u8()
		r.u32()                  // SecurityTokenId
		nl := int(r.u8() & 0x0f) // NonceLength
		r.skip(nl)
		if sf&0x04 != 0 { // FooterEnabled: footer size follows
			r.u16()
		}
		if r.err == nil {
			// Encrypted payload cannot be decoded further.
			if sf&0x01 != 0 {
				nm.DataSetMessages = []*DataSetMessage{{Note: "payload is encrypted (NetworkMessage encryption enabled)"}}
				return nm, nil
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}

	count := len(writerIDs)
	if count == 0 {
		count = 1
	}
	var sizes []int
	if payloadHeaderEnabled && count > 1 {
		for i := 0; i < count; i++ {
			sizes = append(sizes, int(r.u16()))
		}
	}
	if r.err != nil {
		return nil, r.err
	}

	for i := 0; i < count; i++ {
		var body *reader
		if sizes != nil {
			raw := r.take(sizes[i])
			if r.err != nil {
				return nil, r.err
			}
			body = &reader{b: raw}
		} else {
			body = r
		}
		dsm, err := decodeDataSetMessage(body)
		if err != nil {
			return nil, err
		}
		if i < len(writerIDs) {
			id := writerIDs[i]
			dsm.WriterID = &id
		}
		nm.DataSetMessages = append(nm.DataSetMessages, dsm)
	}
	if rest := r.rest(); len(rest) > 0 {
		if len(rest) > 64 {
			nm.UndecodedTrailing = hex.EncodeToString(rest[:64]) + "..."
		} else {
			nm.UndecodedTrailing = hex.EncodeToString(rest)
		}
	}
	return nm, nil
}

func decodeDataSetMessage(r *reader) (*DataSetMessage, error) {
	dsm := &DataSetMessage{}
	f1 := r.u8()
	dsm.Valid = f1&0x01 != 0
	fieldEnc := (f1 >> 1) & 0x03
	switch fieldEnc {
	case 0:
		dsm.FieldEncoding = "Variant"
	case 1:
		dsm.FieldEncoding = "RawData"
	case 2:
		dsm.FieldEncoding = "DataValue"
	default:
		return nil, fmt.Errorf("uadp: reserved field encoding")
	}
	var f2 byte
	if f1&0x80 != 0 {
		f2 = r.u8()
	}
	msgType := f2 & 0x0f
	switch msgType {
	case 0:
		dsm.MessageType = "KeyFrame"
	case 1:
		dsm.MessageType = "DeltaFrame"
	case 2:
		dsm.MessageType = "Event"
	case 3:
		dsm.MessageType = "KeepAlive"
	default:
		return nil, fmt.Errorf("uadp: reserved dataset message type %d", msgType)
	}
	if f1&0x08 != 0 { // sequence number
		v := r.u16()
		dsm.SequenceNumber = &v
	}
	if f2&0x10 != 0 { // timestamp
		dsm.Timestamp = fmtDateTime(r.dateTime())
	}
	if f2&0x20 != 0 { // picoseconds
		r.u16()
	}
	if f1&0x10 != 0 { // status
		v := r.u16()
		dsm.Status = &v
	}
	if f1&0x20 != 0 { // config major version
		v := r.u32()
		dsm.MajorVersion = &v
	}
	if f1&0x40 != 0 { // config minor version
		v := r.u32()
		dsm.MinorVersion = &v
	}
	if r.err != nil {
		return nil, r.err
	}

	switch dsm.MessageType {
	case "KeepAlive":
		return dsm, nil
	case "DeltaFrame":
		count := int(r.u16())
		if count > 4096 {
			return nil, fmt.Errorf("uadp: implausible delta field count %d", count)
		}
		for i := 0; i < count; i++ {
			idx := r.u16()
			v, err := decodeField(r, fieldEnc)
			if err != nil {
				return nil, err
			}
			dsm.Fields = append(dsm.Fields, map[string]any{"index": idx, "value": v})
		}
	case "KeyFrame", "Event":
		if fieldEnc == 1 { // RawData needs external metadata; show hex
			rest := r.rest()
			r.skip(len(rest))
			if len(rest) > 256 {
				dsm.RawDataHex = hex.EncodeToString(rest[:256]) + "..."
			} else {
				dsm.RawDataHex = hex.EncodeToString(rest)
			}
			dsm.Note = "RawData field encoding requires the publisher's DataSetMetaData to decode"
			return dsm, r.err
		}
		count := int(r.u16())
		if count > 4096 {
			return nil, fmt.Errorf("uadp: implausible field count %d", count)
		}
		for i := 0; i < count; i++ {
			v, err := decodeField(r, fieldEnc)
			if err != nil {
				return nil, err
			}
			dsm.Fields = append(dsm.Fields, v)
		}
	}
	return dsm, r.err
}

func decodeField(r *reader, fieldEnc byte) (any, error) {
	if fieldEnc == 2 { // DataValue
		return r.dataValue()
	}
	return r.variant()
}

// --- binary reader --------------------------------------------------------

type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) fail(format string, a ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("uadp: "+format, a...)
	}
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.off+n > len(r.b) {
		r.fail("truncated (need %d bytes at offset %d of %d)", n, r.off, len(r.b))
		return false
	}
	return true
}

func (r *reader) u8() byte {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

func (r *reader) f32() float32 { return math.Float32frombits(r.u32()) }
func (r *reader) f64() float64 { return math.Float64frombits(r.u64()) }

func (r *reader) skip(n int) {
	if n < 0 {
		r.fail("negative skip")
		return
	}
	if !r.need(n) {
		return
	}
	r.off += n
}

func (r *reader) take(n int) []byte {
	if n < 0 || !r.need(n) {
		if n < 0 {
			r.fail("negative length")
		}
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *reader) rest() []byte { return r.b[r.off:] }

// str reads an OPC UA String: int32 length (-1 = null) then UTF-8 bytes.
func (r *reader) str() string {
	n := int32(r.u32())
	if n < 0 {
		return ""
	}
	if n > 1<<20 {
		r.fail("implausible string length %d", n)
		return ""
	}
	raw := r.take(int(n))
	if r.err != nil {
		return ""
	}
	if !utf8.Valid(raw) {
		r.fail("string is not valid UTF-8")
		return ""
	}
	return string(raw)
}

func (r *reader) byteString() any {
	n := int32(r.u32())
	if n < 0 {
		return nil
	}
	if n > 1<<20 {
		r.fail("implausible bytestring length %d", n)
		return nil
	}
	raw := r.take(int(n))
	if r.err != nil {
		return nil
	}
	return hex.EncodeToString(raw)
}

// dateTime reads an OPC UA DateTime (int64, 100 ns ticks since 1601-01-01).
func (r *reader) dateTime() time.Time {
	v := int64(r.u64())
	if v <= 0 {
		return time.Time{}
	}
	const epochDelta = 116444736000000000 // 1601-01-01 → 1970-01-01 in 100ns ticks
	return time.Unix(0, (v-epochDelta)*100).UTC()
}

func fmtDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

func (r *reader) guid() string {
	d1 := r.u32()
	d2 := r.u16()
	d3 := r.u16()
	d4 := r.take(8)
	if r.err != nil {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		d1, d2, d3, d4[0], d4[1], d4[2], d4[3], d4[4], d4[5], d4[6], d4[7])
}

func (r *reader) nodeID() (any, error) {
	enc := r.u8()
	switch enc & 0x3f {
	case 0x00: // two byte
		return fmt.Sprintf("i=%d", r.u8()), nil
	case 0x01: // four byte
		ns := r.u8()
		id := r.u16()
		if ns == 0 {
			return fmt.Sprintf("i=%d", id), nil
		}
		return fmt.Sprintf("ns=%d;i=%d", ns, id), nil
	case 0x02: // numeric
		ns := r.u16()
		id := r.u32()
		return fmt.Sprintf("ns=%d;i=%d", ns, id), nil
	case 0x03: // string
		ns := r.u16()
		s := r.str()
		return fmt.Sprintf("ns=%d;s=%s", ns, s), nil
	case 0x04: // guid
		ns := r.u16()
		g := r.guid()
		return fmt.Sprintf("ns=%d;g=%s", ns, g), nil
	case 0x05: // bytestring
		ns := r.u16()
		bs := r.byteString()
		return fmt.Sprintf("ns=%d;b=%v", ns, bs), nil
	default:
		return nil, fmt.Errorf("uadp: unsupported nodeid encoding 0x%02x", enc)
	}
}

func (r *reader) localizedText() any {
	mask := r.u8()
	out := map[string]any{}
	if mask&0x01 != 0 {
		out["locale"] = r.str()
	}
	if mask&0x02 != 0 {
		out["text"] = r.str()
	}
	if t, ok := out["text"]; ok && len(out) == 1 {
		return t
	}
	return out
}

// scalar decodes one OPC UA built-in scalar value by type id.
func (r *reader) scalar(typeID byte) (any, error) {
	switch typeID {
	case 0:
		return nil, nil
	case 1:
		return r.u8() != 0, nil
	case 2:
		return int8(r.u8()), nil
	case 3:
		return r.u8(), nil
	case 4:
		return int16(r.u16()), nil
	case 5:
		return r.u16(), nil
	case 6:
		return int32(r.u32()), nil
	case 7:
		return r.u32(), nil
	case 8:
		return int64(r.u64()), nil
	case 9:
		return r.u64(), nil
	case 10:
		return r.f32(), nil
	case 11:
		return r.f64(), nil
	case 12:
		return r.str(), nil
	case 13:
		return fmtDateTime(r.dateTime()), nil
	case 14:
		return r.guid(), nil
	case 15:
		return r.byteString(), nil
	case 16: // XmlElement, encoded like ByteString
		return r.byteString(), nil
	case 17:
		return r.nodeID()
	case 19: // StatusCode
		return fmt.Sprintf("0x%08X", r.u32()), nil
	case 20: // QualifiedName
		ns := r.u16()
		name := r.str()
		if ns == 0 {
			return name, nil
		}
		return fmt.Sprintf("%d:%s", ns, name), nil
	case 21:
		return r.localizedText(), nil
	case 23:
		return r.dataValue()
	case 24: // nested Variant
		return r.variant()
	default:
		return nil, fmt.Errorf("uadp: unsupported built-in type %d", typeID)
	}
}

// variant decodes an OPC UA Variant: encoding byte, then scalar or array.
func (r *reader) variant() (any, error) {
	enc := r.u8()
	if r.err != nil {
		return nil, r.err
	}
	typeID := enc & 0x3f
	if enc&0x80 == 0 { // scalar
		v, err := r.scalar(typeID)
		if err != nil {
			return nil, err
		}
		return v, r.err
	}
	n := int32(r.u32())
	if n < 0 {
		return nil, r.err
	}
	if n > 1<<16 {
		return nil, fmt.Errorf("uadp: implausible array length %d", n)
	}
	arr := make([]any, 0, n)
	for i := int32(0); i < n; i++ {
		v, err := r.scalar(typeID)
		if err != nil {
			return nil, err
		}
		if r.err != nil {
			return nil, r.err
		}
		arr = append(arr, v)
	}
	if enc&0x40 != 0 { // array dimensions
		dims := int32(r.u32())
		if dims > 0 && dims < 16 {
			for i := int32(0); i < dims; i++ {
				r.u32()
			}
		}
	}
	return arr, r.err
}

// dataValue decodes an OPC UA DataValue.
func (r *reader) dataValue() (any, error) {
	mask := r.u8()
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]any{}
	if mask&0x01 != 0 {
		v, err := r.variant()
		if err != nil {
			return nil, err
		}
		out["value"] = v
	}
	if mask&0x02 != 0 {
		out["status"] = fmt.Sprintf("0x%08X", r.u32())
	}
	if mask&0x04 != 0 {
		out["source_timestamp"] = fmtDateTime(r.dateTime())
	}
	if mask&0x08 != 0 {
		out["server_timestamp"] = fmtDateTime(r.dateTime())
	}
	if mask&0x10 != 0 { // source picoseconds
		r.u16()
	}
	if mask&0x20 != 0 { // server picoseconds
		r.u16()
	}
	return out, r.err
}
