// Package sparkplug decodes Eclipse Sparkplug B payloads (the
// org.eclipse.tahu.protobuf.Payload message) directly from the protobuf wire
// format, so no generated code or .proto file is needed at build time.
package sparkplug

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Sparkplug B metric datatype identifiers (Sparkplug 3.0 specification).
const (
	TypeInt8            = 1
	TypeInt16           = 2
	TypeInt32           = 3
	TypeInt64           = 4
	TypeUInt8           = 5
	TypeUInt16          = 6
	TypeUInt32          = 7
	TypeUInt64          = 8
	TypeFloat           = 9
	TypeDouble          = 10
	TypeBoolean         = 11
	TypeString          = 12
	TypeDateTime        = 13
	TypeText            = 14
	TypeUUID            = 15
	TypeDataSet         = 16
	TypeBytes           = 17
	TypeFile            = 18
	TypeTemplate        = 19
	TypePropertySet     = 20
	TypePropertySetList = 21
	TypeInt8Array       = 22
	TypeInt16Array      = 23
	TypeInt32Array      = 24
	TypeInt64Array      = 25
	TypeUInt8Array      = 26
	TypeUInt16Array     = 27
	TypeUInt32Array     = 28
	TypeUInt64Array     = 29
	TypeFloatArray      = 30
	TypeDoubleArray     = 31
	TypeBooleanArray    = 32
	TypeStringArray     = 33
	TypeDateTimeArray   = 34
)

var typeNames = map[uint32]string{
	TypeInt8: "Int8", TypeInt16: "Int16", TypeInt32: "Int32", TypeInt64: "Int64",
	TypeUInt8: "UInt8", TypeUInt16: "UInt16", TypeUInt32: "UInt32", TypeUInt64: "UInt64",
	TypeFloat: "Float", TypeDouble: "Double", TypeBoolean: "Boolean", TypeString: "String",
	TypeDateTime: "DateTime", TypeText: "Text", TypeUUID: "UUID", TypeDataSet: "DataSet",
	TypeBytes: "Bytes", TypeFile: "File", TypeTemplate: "Template",
	TypePropertySet: "PropertySet", TypePropertySetList: "PropertySetList",
	TypeInt8Array: "Int8Array", TypeInt16Array: "Int16Array", TypeInt32Array: "Int32Array",
	TypeInt64Array: "Int64Array", TypeUInt8Array: "UInt8Array", TypeUInt16Array: "UInt16Array",
	TypeUInt32Array: "UInt32Array", TypeUInt64Array: "UInt64Array", TypeFloatArray: "FloatArray",
	TypeDoubleArray: "DoubleArray", TypeBooleanArray: "BooleanArray", TypeStringArray: "StringArray",
	TypeDateTimeArray: "DateTimeArray",
}

// TypeName returns the Sparkplug name for a datatype id.
func TypeName(t uint32) string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return fmt.Sprintf("Unknown(%d)", t)
}

// Payload is a decoded Sparkplug B payload.
type Payload struct {
	Timestamp *uint64  `json:"timestamp,omitempty"`
	Metrics   []Metric `json:"metrics,omitempty"`
	Seq       *uint64  `json:"seq,omitempty"`
	UUID      string   `json:"uuid,omitempty"`
	Body      []byte   `json:"body,omitempty"`
}

// Metric is a decoded Sparkplug B metric.
type Metric struct {
	Name         string         `json:"name,omitempty"`
	Alias        *uint64        `json:"alias,omitempty"`
	Timestamp    *uint64        `json:"timestamp,omitempty"`
	DataType     uint32         `json:"-"`
	IsHistorical bool           `json:"is_historical,omitempty"`
	IsTransient  bool           `json:"is_transient,omitempty"`
	IsNull       bool           `json:"is_null,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Properties   map[string]any `json:"properties,omitempty"`
	Value        any            `json:"value"`
}

// DataSet is a decoded Sparkplug B dataset value.
type DataSet struct {
	Columns []string `json:"columns"`
	Types   []string `json:"types"`
	Rows    [][]any  `json:"rows"`
}

// Template is a decoded Sparkplug B template value.
type Template struct {
	Version      string         `json:"version,omitempty"`
	TemplateRef  string         `json:"template_ref,omitempty"`
	IsDefinition bool           `json:"is_definition,omitempty"`
	Metrics      []Metric       `json:"metrics,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
}

// TopicInfo is a parsed Sparkplug B topic.
type TopicInfo struct {
	GroupID     string
	MessageType string // NBIRTH NDEATH DBIRTH DDEATH NDATA DDATA NCMD DCMD STATE
	EdgeNodeID  string
	DeviceID    string
}

// IsSparkplugTopic reports whether the topic sits in the spBv1.0 namespace.
func IsSparkplugTopic(topic string) bool {
	return strings.HasPrefix(topic, "spBv1.0/")
}

// ParseTopic splits a Sparkplug B topic into its components.
func ParseTopic(topic string) (TopicInfo, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[0] != "spBv1.0" {
		return TopicInfo{}, false
	}
	if parts[1] == "STATE" {
		return TopicInfo{MessageType: "STATE", EdgeNodeID: parts[2]}, true
	}
	if len(parts) < 4 {
		return TopicInfo{}, false
	}
	ti := TopicInfo{GroupID: parts[1], MessageType: parts[2], EdgeNodeID: parts[3]}
	if len(parts) > 4 {
		ti.DeviceID = parts[4]
	}
	return ti, true
}

// EdgeKey identifies the edge node (and device) a topic belongs to, used to
// scope alias maps learned from BIRTH messages.
func (t TopicInfo) EdgeKey() string {
	return t.GroupID + "/" + t.EdgeNodeID + "/" + t.DeviceID
}

// IsBirth reports whether the message type carries metric alias definitions.
func (t TopicInfo) IsBirth() bool {
	return t.MessageType == "NBIRTH" || t.MessageType == "DBIRTH"
}

// Decode parses a Sparkplug B payload from raw bytes.
func Decode(b []byte) (*Payload, error) {
	p := &Payload{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug: bad tag: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1: // timestamp
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			p.Timestamp = &v
			b = b[n:]
		case 2: // metrics
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			m, err := decodeMetric(raw)
			if err != nil {
				return nil, err
			}
			p.Metrics = append(p.Metrics, m)
			b = b[n:]
		case 3: // seq
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			p.Seq = &v
			b = b[n:]
		case 4: // uuid
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			p.UUID = string(raw)
			b = b[n:]
		case 5: // body
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			p.Body = append([]byte(nil), raw...)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug: bad field %d: %w", num, protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return p, nil
}

func decodeMetric(b []byte) (Metric, error) {
	m := Metric{}
	var rawInt, rawLong uint64
	var rawFloat float32
	var rawDouble float64
	var rawBool bool
	var rawString string
	var rawBytes []byte
	var rawDataSet *DataSet
	var rawTemplate *Template
	valueField := 0

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return m, fmt.Errorf("sparkplug metric: bad tag: %w", protowire.ParseError(n))
		}
		b = b[n:]
		var err error
		var adv int
		switch num {
		case 1: // name
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			m.Name = string(raw)
		case 2: // alias
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.Alias = &v
		case 3: // timestamp
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.Timestamp = &v
		case 4: // datatype
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.DataType = uint32(v)
		case 5: // is_historical
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.IsHistorical = v != 0
		case 6: // is_transient
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.IsTransient = v != 0
		case 7: // is_null
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			m.IsNull = v != 0
		case 8: // metadata
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			if err == nil {
				m.Metadata, err = decodeMetaData(raw)
			}
		case 9: // properties
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			if err == nil {
				m.Properties, err = decodePropertySet(raw)
			}
		case 10:
			rawInt, adv, err = consumeVarint(b, typ)
			valueField = 10
		case 11:
			rawLong, adv, err = consumeVarint(b, typ)
			valueField = 11
		case 12:
			var v uint64
			v, adv, err = consumeFixed32(b, typ)
			rawFloat = math.Float32frombits(uint32(v))
			valueField = 12
		case 13:
			var v uint64
			v, adv, err = consumeFixed64(b, typ)
			rawDouble = math.Float64frombits(v)
			valueField = 13
		case 14:
			var v uint64
			v, adv, err = consumeVarint(b, typ)
			rawBool = v != 0
			valueField = 14
		case 15:
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			rawString = string(raw)
			valueField = 15
		case 16:
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			rawBytes = append([]byte(nil), raw...)
			valueField = 16
		case 17:
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			if err == nil {
				rawDataSet, err = decodeDataSet(raw)
			}
			valueField = 17
		case 18:
			var raw []byte
			raw, adv, err = consumeBytes(b, typ)
			if err == nil {
				rawTemplate, err = decodeTemplate(raw)
			}
			valueField = 18
		default:
			adv = protowire.ConsumeFieldValue(num, typ, b)
			if adv < 0 {
				return m, fmt.Errorf("sparkplug metric: bad field %d: %w", num, protowire.ParseError(adv))
			}
		}
		if err != nil {
			return m, err
		}
		b = b[adv:]
	}

	if m.IsNull {
		m.Value = nil
		return m, nil
	}
	switch valueField {
	case 10:
		m.Value = convertIntValue(m.DataType, uint32(rawInt))
	case 11:
		m.Value = convertLongValue(m.DataType, rawLong)
	case 12:
		m.Value = rawFloat
	case 13:
		m.Value = rawDouble
	case 14:
		m.Value = rawBool
	case 15:
		m.Value = rawString
	case 16:
		m.Value = convertBytesValue(m.DataType, rawBytes)
	case 17:
		m.Value = rawDataSet
	case 18:
		m.Value = rawTemplate
	}
	return m, nil
}

// convertIntValue re-signs the uint32 wire value according to the datatype.
func convertIntValue(datatype, v uint32) any {
	switch datatype {
	case TypeInt8:
		return int8(v)
	case TypeInt16:
		return int16(v)
	case TypeInt32:
		return int32(v)
	default:
		return v
	}
}

func convertLongValue(datatype uint32, v uint64) any {
	switch datatype {
	case TypeInt64:
		return int64(v)
	case TypeDateTime, TypeDateTimeArray:
		return formatSparkplugTime(v)
	default:
		return v
	}
}

func formatSparkplugTime(ms uint64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

// convertBytesValue decodes packed array types carried in bytes_value
// (Sparkplug 3.0), falling back to base64 for opaque bytes.
func convertBytesValue(datatype uint32, b []byte) any {
	le := func(i, width int) uint64 {
		var v uint64
		for k := 0; k < width; k++ {
			v |= uint64(b[i+k]) << (8 * k)
		}
		return v
	}
	numArray := func(width int, conv func(uint64) any) any {
		if len(b)%width != 0 {
			return base64.StdEncoding.EncodeToString(b)
		}
		out := make([]any, 0, len(b)/width)
		for i := 0; i+width <= len(b); i += width {
			out = append(out, conv(le(i, width)))
		}
		return out
	}
	switch datatype {
	case TypeInt8Array:
		return numArray(1, func(v uint64) any { return int8(v) })
	case TypeUInt8Array:
		return numArray(1, func(v uint64) any { return uint8(v) })
	case TypeInt16Array:
		return numArray(2, func(v uint64) any { return int16(v) })
	case TypeUInt16Array:
		return numArray(2, func(v uint64) any { return uint16(v) })
	case TypeInt32Array:
		return numArray(4, func(v uint64) any { return int32(v) })
	case TypeUInt32Array:
		return numArray(4, func(v uint64) any { return uint32(v) })
	case TypeInt64Array:
		return numArray(8, func(v uint64) any { return int64(v) })
	case TypeUInt64Array, TypeDateTimeArray:
		if datatype == TypeDateTimeArray {
			return numArray(8, func(v uint64) any { return formatSparkplugTime(v) })
		}
		return numArray(8, func(v uint64) any { return v })
	case TypeFloatArray:
		return numArray(4, func(v uint64) any { return math.Float32frombits(uint32(v)) })
	case TypeDoubleArray:
		return numArray(8, func(v uint64) any { return math.Float64frombits(v) })
	case TypeBooleanArray:
		// 4-byte little-endian count, then packed bits (MSB first per byte).
		if len(b) < 4 {
			return base64.StdEncoding.EncodeToString(b)
		}
		count := int(le(0, 4))
		bits := b[4:]
		if count < 0 || count > len(bits)*8 {
			return base64.StdEncoding.EncodeToString(b)
		}
		out := make([]any, 0, count)
		for i := 0; i < count; i++ {
			out = append(out, bits[i/8]&(0x80>>(i%8)) != 0)
		}
		return out
	case TypeStringArray:
		// Null-terminated strings.
		parts := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		out := make([]any, len(parts))
		for i, s := range parts {
			out[i] = s
		}
		return out
	default:
		return base64.StdEncoding.EncodeToString(b)
	}
}

func decodeMetaData(b []byte) (map[string]any, error) {
	out := map[string]any{}
	fields := map[protowire.Number]string{
		1: "is_multi_part", 2: "content_type", 3: "size", 4: "seq",
		5: "file_name", 6: "file_type", 7: "md5", 8: "description",
	}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug metadata: %w", protowire.ParseError(n))
		}
		b = b[n:]
		name := fields[num]
		switch num {
		case 1:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			out[name] = v != 0
			b = b[n:]
		case 3, 4:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			out[name] = v
			b = b[n:]
		case 2, 5, 6, 7, 8:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			out[name] = string(raw)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug metadata: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return out, nil
}

func decodePropertySet(b []byte) (map[string]any, error) {
	var keys []string
	var values []any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug propertyset: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1: // keys
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			keys = append(keys, string(raw))
			b = b[n:]
		case 2: // values
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			v, err := decodePropertyValue(raw)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug propertyset: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	out := make(map[string]any, len(keys))
	for i, k := range keys {
		if i < len(values) {
			out[k] = values[i]
		}
	}
	return out, nil
}

func decodePropertyValue(b []byte) (any, error) {
	var datatype uint32
	var isNull bool
	var value any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug propertyvalue: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			datatype = uint32(v)
			b = b[n:]
		case 2:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			isNull = v != 0
			b = b[n:]
		case 3:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = convertIntValue(datatype, uint32(v))
			b = b[n:]
		case 4:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = convertLongValue(datatype, v)
			b = b[n:]
		case 5:
			v, n, err := consumeFixed32(b, typ)
			if err != nil {
				return nil, err
			}
			value = math.Float32frombits(uint32(v))
			b = b[n:]
		case 6:
			v, n, err := consumeFixed64(b, typ)
			if err != nil {
				return nil, err
			}
			value = math.Float64frombits(v)
			b = b[n:]
		case 7:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = v != 0
			b = b[n:]
		case 8:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			value = string(raw)
			b = b[n:]
		case 9: // nested propertyset
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			value, err = decodePropertySet(raw)
			if err != nil {
				return nil, err
			}
			b = b[n:]
		case 10: // propertyset list
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			value, err = decodePropertySetList(raw)
			if err != nil {
				return nil, err
			}
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug propertyvalue: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	if isNull {
		return nil, nil
	}
	return value, nil
}

func decodePropertySetList(b []byte) ([]any, error) {
	var out []any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug propertysetlist: %w", protowire.ParseError(n))
		}
		b = b[n:]
		if num == 1 {
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			ps, err := decodePropertySet(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, ps)
			b = b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug propertysetlist: %w", protowire.ParseError(n))
		}
		b = b[n:]
	}
	return out, nil
}

func decodeDataSet(b []byte) (*DataSet, error) {
	ds := &DataSet{Columns: []string{}, Types: []string{}, Rows: [][]any{}}
	var types []uint32
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug dataset: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1: // num_of_columns
			_, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			b = b[n:]
		case 2: // columns
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			ds.Columns = append(ds.Columns, string(raw))
			b = b[n:]
		case 3: // types (repeated uint32, may be packed)
			if typ == protowire.BytesType {
				raw, n, err := consumeBytes(b, typ)
				if err != nil {
					return nil, err
				}
				for len(raw) > 0 {
					v, n2 := protowire.ConsumeVarint(raw)
					if n2 < 0 {
						return nil, fmt.Errorf("sparkplug dataset types: %w", protowire.ParseError(n2))
					}
					types = append(types, uint32(v))
					raw = raw[n2:]
				}
				b = b[n:]
			} else {
				v, n, err := consumeVarint(b, typ)
				if err != nil {
					return nil, err
				}
				types = append(types, uint32(v))
				b = b[n:]
			}
		case 4: // rows
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			row, err := decodeDataSetRow(raw, types)
			if err != nil {
				return nil, err
			}
			ds.Rows = append(ds.Rows, row)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug dataset: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	for _, t := range types {
		ds.Types = append(ds.Types, TypeName(t))
	}
	return ds, nil
}

func decodeDataSetRow(b []byte, types []uint32) ([]any, error) {
	var row []any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug row: %w", protowire.ParseError(n))
		}
		b = b[n:]
		if num == 1 {
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			var colType uint32
			if len(row) < len(types) {
				colType = types[len(row)]
			}
			v, err := decodeDataSetValue(raw, colType)
			if err != nil {
				return nil, err
			}
			row = append(row, v)
			b = b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug row: %w", protowire.ParseError(n))
		}
		b = b[n:]
	}
	return row, nil
}

func decodeDataSetValue(b []byte, colType uint32) (any, error) {
	var value any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug datasetvalue: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = convertIntValue(colType, uint32(v))
			b = b[n:]
		case 2:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = convertLongValue(colType, v)
			b = b[n:]
		case 3:
			v, n, err := consumeFixed32(b, typ)
			if err != nil {
				return nil, err
			}
			value = math.Float32frombits(uint32(v))
			b = b[n:]
		case 4:
			v, n, err := consumeFixed64(b, typ)
			if err != nil {
				return nil, err
			}
			value = math.Float64frombits(v)
			b = b[n:]
		case 5:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			value = v != 0
			b = b[n:]
		case 6:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			value = string(raw)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug datasetvalue: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return value, nil
}

func decodeTemplate(b []byte) (*Template, error) {
	t := &Template{Parameters: map[string]any{}}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("sparkplug template: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			t.Version = string(raw)
			b = b[n:]
		case 2:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			m, err := decodeMetric(raw)
			if err != nil {
				return nil, err
			}
			t.Metrics = append(t.Metrics, m)
			b = b[n:]
		case 3:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			name, val, err := decodeParameter(raw)
			if err != nil {
				return nil, err
			}
			t.Parameters[name] = val
			b = b[n:]
		case 4:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return nil, err
			}
			t.TemplateRef = string(raw)
			b = b[n:]
		case 5:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return nil, err
			}
			t.IsDefinition = v != 0
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("sparkplug template: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	if len(t.Parameters) == 0 {
		t.Parameters = nil
	}
	return t, nil
}

func decodeParameter(b []byte) (string, any, error) {
	var name string
	var datatype uint32
	var value any
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", nil, fmt.Errorf("sparkplug parameter: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case 1:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return "", nil, err
			}
			name = string(raw)
			b = b[n:]
		case 2:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return "", nil, err
			}
			datatype = uint32(v)
			b = b[n:]
		case 3:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = convertIntValue(datatype, uint32(v))
			b = b[n:]
		case 4:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = convertLongValue(datatype, v)
			b = b[n:]
		case 5:
			v, n, err := consumeFixed32(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = math.Float32frombits(uint32(v))
			b = b[n:]
		case 6:
			v, n, err := consumeFixed64(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = math.Float64frombits(v)
			b = b[n:]
		case 7:
			v, n, err := consumeVarint(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = v != 0
			b = b[n:]
		case 8:
			raw, n, err := consumeBytes(b, typ)
			if err != nil {
				return "", nil, err
			}
			value = string(raw)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return "", nil, fmt.Errorf("sparkplug parameter: %w", protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return name, value, nil
}

// HarvestAliases extracts alias→name mappings from a BIRTH payload.
func HarvestAliases(p *Payload) map[uint64]string {
	out := map[uint64]string{}
	for _, m := range p.Metrics {
		if m.Alias != nil && m.Name != "" {
			out[*m.Alias] = m.Name
		}
	}
	return out
}

// ResolveAliases fills in metric names from a previously learned alias map
// (aliases are announced in NBIRTH/DBIRTH and omitted from NDATA/DDATA).
func ResolveAliases(p *Payload, aliases map[uint64]string) {
	if len(aliases) == 0 {
		return
	}
	for i := range p.Metrics {
		m := &p.Metrics[i]
		if m.Name == "" && m.Alias != nil {
			if name, ok := aliases[*m.Alias]; ok {
				m.Name = name
			}
		}
	}
}

// --- wire helpers ---------------------------------------------------------

func consumeVarint(b []byte, typ protowire.Type) (uint64, int, error) {
	if typ != protowire.VarintType {
		return 0, 0, fmt.Errorf("sparkplug: expected varint, got wire type %d", typ)
	}
	v, n := protowire.ConsumeVarint(b)
	if n < 0 {
		return 0, 0, protowire.ParseError(n)
	}
	return v, n, nil
}

func consumeFixed32(b []byte, typ protowire.Type) (uint64, int, error) {
	if typ != protowire.Fixed32Type {
		return 0, 0, fmt.Errorf("sparkplug: expected fixed32, got wire type %d", typ)
	}
	v, n := protowire.ConsumeFixed32(b)
	if n < 0 {
		return 0, 0, protowire.ParseError(n)
	}
	return uint64(v), n, nil
}

func consumeFixed64(b []byte, typ protowire.Type) (uint64, int, error) {
	if typ != protowire.Fixed64Type {
		return 0, 0, fmt.Errorf("sparkplug: expected fixed64, got wire type %d", typ)
	}
	v, n := protowire.ConsumeFixed64(b)
	if n < 0 {
		return 0, 0, protowire.ParseError(n)
	}
	return v, n, nil
}

func consumeBytes(b []byte, typ protowire.Type) ([]byte, int, error) {
	if typ != protowire.BytesType {
		return nil, 0, fmt.Errorf("sparkplug: expected bytes, got wire type %d", typ)
	}
	v, n := protowire.ConsumeBytes(b)
	if n < 0 {
		return nil, 0, protowire.ParseError(n)
	}
	return v, n, nil
}
