package enrich

import (
	"encoding/base64"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MarkRosemaker/asyncapi"
)

// detectBinaryEncodings walks every schema reachable from doc.Components.Schemas
// and marks a string field whose recorded examples are all base64 and decode
// to a self-consistent raw protobuf byte stream — see [parseProtoWire] — with
// ContentEncoding "base64", documenting what the wire format itself revealed
// in Description.
//
// This exists because a WebSocket API is free to put anything inside a JSON
// string, and at least one real one (Yahoo Finance's pricing feed) puts
// base64-encoded protobuf there. AsyncAPI's JSON Schema has no keyword for
// "this is protobuf", and this package has no .proto to check against — only
// the bytes themselves and however many samples were recorded. What it
// asserts is limited to what those bytes prove structurally: that they are
// valid base64, and that decoding them yields a byte stream whose protobuf
// tags agree with each other across every sample. It does not guess field
// names or semantics; a human with domain knowledge (or a real .proto) is
// still needed for that, and gets a head start from the field-by-field
// breakdown in Description instead of starting from an opaque string.
func detectBinaryEncodings(doc *asyncapi.Document) {
	for _, ref := range doc.Components.Schemas {
		walkSchema(ref.Value.Schema, detectSchemaBinary)
	}
}

// walkSchema calls fn on s and on every schema reachable from it.
func walkSchema(s *asyncapi.Schema, fn func(*asyncapi.Schema)) {
	if s == nil {
		return
	}

	fn(s)

	for _, ref := range s.Properties {
		walkSchema(ref.Value.Schema, fn)
	}

	if s.Items != nil {
		walkSchema(s.Items.Value.Schema, fn)
	}

	if s.AdditionalProperties != nil {
		walkSchema(s.AdditionalProperties.Value.Schema, fn)
	}
}

// minSamplesForBinaryDetection is how many recorded examples must agree
// before a string field is called protobuf-shaped base64. One sample parsing
// as valid protobuf wire format is already unlikely by chance, but this
// package would rather say nothing than call a coincidence a pattern.
const minSamplesForBinaryDetection = 2

func detectSchemaBinary(s *asyncapi.Schema) {
	if !s.Type.Contains(asyncapi.TypeString) || s.Format != "" || s.ContentEncoding != "" {
		return
	}

	if len(s.Examples) < minSamplesForBinaryDetection {
		return
	}

	samples := make([][]byte, 0, len(s.Examples))

	for _, ex := range s.Examples {
		var str string
		if err := json.Unmarshal(ex, &str); err != nil {
			return
		}

		data, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			return
		}

		samples = append(samples, data)
	}

	layout, ok := consistentProtoLayout(samples)
	if !ok {
		return
	}

	s.ContentEncoding = "base64"
	s.Description = mergeString(s.Description, describeProtoLayout(layout))
}

// protoFieldSamples is what every recorded sample that carried a given
// protobuf field number agreed on — its wire type — plus the value from each
// sample that carried it, in recording order.
type protoFieldSamples struct {
	number   int
	wireType int
	seenIn   int
	values   []protoField
}

// consistentProtoLayout parses every sample as raw protobuf wire format and
// reports the field layout only if every sample parses cleanly (no leftover
// bytes) and every field number that appears in more than one sample carries
// the same wire type each time it does — protobuf's tag byte would not vary
// the wire type of the same field number between messages of the same type,
// so a mismatch means these bytes are not what they appear to be.
func consistentProtoLayout(samples [][]byte) ([]protoFieldSamples, bool) {
	wireTypeByNumber := map[int]int{}
	valuesByNumber := map[int][]protoField{}
	seenByNumber := map[int]int{}

	for _, data := range samples {
		fields, err := parseProtoWire(data)
		if err != nil || len(fields) == 0 {
			return nil, false
		}

		seenThisSample := map[int]bool{}

		for _, f := range fields {
			if wt, ok := wireTypeByNumber[f.Number]; ok {
				if wt != f.WireType {
					return nil, false
				}
			} else {
				wireTypeByNumber[f.Number] = f.WireType
			}

			seenThisSample[f.Number] = true
			valuesByNumber[f.Number] = append(valuesByNumber[f.Number], f)
		}

		for n := range seenThisSample {
			seenByNumber[n]++
		}
	}

	numbers := make([]int, 0, len(wireTypeByNumber))
	for n := range wireTypeByNumber {
		numbers = append(numbers, n)
	}

	sort.Ints(numbers)

	out := make([]protoFieldSamples, 0, len(numbers))

	for _, n := range numbers {
		out = append(out, protoFieldSamples{
			number:   n,
			wireType: wireTypeByNumber[n],
			seenIn:   seenByNumber[n],
			values:   valuesByNumber[n],
		})
	}

	return out, true
}

// describeProtoLayout renders the field-by-field evidence consistentProtoLayout
// found into prose, for [asyncapi.Schema.Description]. It states only what the
// bytes themselves show — value, wire type, whether a bytes field decodes as
// UTF-8, whether a numeric field only ever increased — because anything past
// that (what the field means) is not something the recording can prove.
func describeProtoLayout(layout []protoFieldSamples) string {
	var b strings.Builder

	b.WriteString("Reverse-engineered from recorded protobuf-in-base64 payloads. " +
		"There is no verified .proto source for this — field numbers and raw " +
		"values below are what the wire format itself proved consistent across " +
		"every recorded sample; field meanings are not asserted.\n")

	nSamples := 0
	for _, f := range layout {
		if f.seenIn > nSamples {
			nSamples = f.seenIn
		}
	}

	for _, f := range layout {
		b.WriteString("\n  field ")
		fmt.Fprintf(&b, "%d", f.number)
		b.WriteString(" (")
		b.WriteString(wireTypeName(f.wireType))
		b.WriteString(")")

		if f.seenIn < nSamples {
			fmt.Fprintf(&b, ", seen in %d of %d samples", f.seenIn, nSamples)
		}

		b.WriteString(": ")
		b.WriteString(describeFieldValues(f))
	}

	return b.String()
}

func wireTypeName(wt int) string {
	switch wt {
	case protoWireVarint:
		return "varint"
	case protoWireFixed64:
		return "64-bit"
	case protoWireBytes:
		return "length-delimited"
	case protoWireFixed32:
		return "32-bit"
	default:
		return "unknown"
	}
}

// describeFieldValues renders the observed values of one field, noting the
// structural properties consistentProtoLayout is positioned to actually
// prove: that every sample's bytes were valid UTF-8, or that a numeric value
// never went down across samples (recording order is the order these frames
// arrived in, so "never decreased" here means exactly that, not a guess).
func describeFieldValues(f protoFieldSamples) string {
	switch f.wireType {
	case protoWireBytes:
		allUTF8 := true

		strs := make([]string, len(f.values))

		for i, v := range f.values {
			if !utf8.Valid(v.raw) {
				allUTF8 = false
			}

			strs[i] = fmt.Sprintf("%q", string(v.raw))
		}

		if allUTF8 {
			return "string-like, e.g. " + strings.Join(dedupLimit(strs, 3), ", ")
		}

		hexes := make([]string, len(f.values))
		for i, v := range f.values {
			hexes[i] = fmt.Sprintf("%x", v.raw)
		}

		return "raw bytes, e.g. " + strings.Join(dedupLimit(hexes, 3), ", ")

	case protoWireVarint:
		return describeNumeric(f, func(v protoField) float64 { return float64(v.uvarint) }, false)

	case protoWireFixed32:
		return describeNumeric(f, func(v protoField) float64 { return float64(v.Float32()) }, true)

	case protoWireFixed64:
		return describeNumeric(f, func(v protoField) float64 { return v.Float64() }, true)

	default:
		return "(unrecognized wire type)"
	}
}

func describeNumeric(f protoFieldSamples, asFloat func(protoField) float64, isFloatWireType bool) string {
	nums := make([]string, len(f.values))
	monotonic := true

	for i, v := range f.values {
		if isFloatWireType {
			nums[i] = fmt.Sprintf("%v", asFloat(v))
		} else {
			nums[i] = fmt.Sprintf("%d", v.uvarint)
		}

		if i > 0 && asFloat(v) < asFloat(f.values[i-1]) {
			monotonic = false
		}
	}

	desc := strings.Join(dedupLimit(nums, 3), ", ")

	if monotonic && len(f.values) >= minSamplesForBinaryDetection && nums[0] != nums[len(nums)-1] {
		desc += " (never decreased across samples — consistent with a running counter)"
	}

	return desc
}

// dedupLimit returns the first n distinct strings of ss, in order.
func dedupLimit(ss []string, n int) []string {
	seen := make(map[string]bool, len(ss))

	out := make([]string, 0, min(n, len(ss)))

	for _, s := range ss {
		if seen[s] {
			continue
		}

		seen[s] = true

		out = append(out, s)

		if len(out) == n {
			break
		}
	}

	return out
}

// Protobuf wire types, per https://protobuf.dev/programming-guides/encoding/#structure.
// Types 3 and 4 (start/end group) are deprecated and unused by any protoc
// released this century; a tag naming them, or any other value, is treated as
// proof these bytes are not protobuf.
const (
	protoWireVarint  = 0
	protoWireFixed64 = 1
	protoWireBytes   = 2
	protoWireFixed32 = 5
)

// protoField is one field parseProtoWire found by reading data as raw
// protobuf wire format, with no schema — the tag byte names its own field
// number and wire type, so no .proto is needed to find this much.
type protoField struct {
	Number   int
	WireType int

	raw     []byte // set when WireType == protoWireBytes
	uvarint uint64 // set when WireType == protoWireVarint
	fixed64 uint64 // set when WireType == protoWireFixed64
	fixed32 uint32 // set when WireType == protoWireFixed32
}

// Float32 reinterprets a fixed32 field's bits as an IEEE-754 float32 — one of
// two things protobuf ever puts in a fixed32 (the other is a plain uint32);
// describeFieldValues reports this reading since a fractional value is what a
// price, percentage, or similar measurement looks like on the wire.
func (f protoField) Float32() float32 {
	return math.Float32frombits(f.fixed32)
}

// Float64 reinterprets a fixed64 field's bits as an IEEE-754 float64.
func (f protoField) Float64() float64 {
	return math.Float64frombits(f.fixed64)
}

// parseProtoWire decodes data as a raw protobuf wire-format byte stream: a
// sequence of (field number, wire type) tags, each followed by its value, per
// the Protocol Buffers encoding spec. It needs no .proto — the tag byte names
// its own field number and wire type — but it does need every byte in data to
// belong to a well-formed tag/value pair; arbitrary bytes overwhelmingly fail
// that, which is what makes a clean parse meaningful evidence rather than a
// coincidence.
func parseProtoWire(data []byte) ([]protoField, error) {
	var out []protoField

	i := 0
	for i < len(data) {
		tag, n, err := readVarint(data[i:])
		if err != nil {
			return nil, fmt.Errorf("tag at byte %d: %w", i, err)
		}

		i += n

		number := int(tag >> 3)
		wireType := int(tag & 0x7)

		if number == 0 {
			return nil, fmt.Errorf("field number 0 at byte %d", i)
		}

		f := protoField{Number: number, WireType: wireType}

		switch wireType {
		case protoWireVarint:
			v, n, err := readVarint(data[i:])
			if err != nil {
				return nil, fmt.Errorf("varint at byte %d: %w", i, err)
			}

			f.uvarint = v
			i += n

		case protoWireFixed64:
			if i+8 > len(data) {
				return nil, fmt.Errorf("fixed64 at byte %d: truncated", i)
			}

			f.fixed64 = binary.LittleEndian.Uint64(data[i : i+8])
			i += 8

		case protoWireBytes:
			length, n, err := readVarint(data[i:])
			if err != nil {
				return nil, fmt.Errorf("length at byte %d: %w", i, err)
			}

			i += n

			if length > uint64(len(data)-i) {
				return nil, fmt.Errorf("bytes at byte %d: length %d runs past end", i, length)
			}

			f.raw = data[i : i+int(length)]
			i += int(length)

		case protoWireFixed32:
			if i+4 > len(data) {
				return nil, fmt.Errorf("fixed32 at byte %d: truncated", i)
			}

			f.fixed32 = binary.LittleEndian.Uint32(data[i : i+4])
			i += 4

		default:
			return nil, fmt.Errorf("wire type %d at byte %d: not a protobuf wire type", wireType, i)
		}

		out = append(out, f)
	}

	return out, nil
}

// readVarint decodes a base-128 varint from the start of data, per protobuf's
// encoding. It caps at 10 bytes — the most a 64-bit varint ever needs — so
// non-protobuf bytes cannot spin it into reading past a sane bound.
func readVarint(data []byte) (uint64, int, error) {
	var result uint64

	for i := 0; i < len(data) && i < 10; i++ {
		b := data[i]
		result |= uint64(b&0x7f) << (7 * i)

		if b&0x80 == 0 {
			return result, i + 1, nil
		}
	}

	return 0, 0, errors.New("truncated or oversized varint")
}
