package enrich

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
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
// ContentEncoding "base64", describing the data found there in Description.
//
// This exists because a WebSocket API is free to put anything inside a JSON
// string, and at least one real one (Yahoo Finance's pricing feed) puts
// base64-encoded protobuf there. AsyncAPI's JSON Schema has no keyword for
// "this is protobuf", and this package has no .proto to check against — only
// the bytes themselves, however many samples were recorded, and (via ss) what
// the client itself sent, which sometimes shows back up in what it receives.
// What Description says is limited to what that evidence proves: it does not
// invent field names, and a genuine ambiguity in the wire format — a varint
// can be a plain integer or a zigzag-encoded signed one, and nothing in the
// bytes says which — is reported as exactly that, not guessed away.
func detectBinaryEncodings(doc *asyncapi.Document, ss Sessions) {
	sent := collectSentStrings(ss)

	for _, ref := range doc.Components.Schemas {
		walkSchema(ref.Value.Schema, func(s *asyncapi.Schema) { detectSchemaBinary(s, sent) })
	}
}

// collectSentStrings returns every string value found anywhere in a "send"
// frame, or in the unsubscribe payload, across every session. A base64 field
// this package decodes may echo one of these back — Yahoo Finance's pricing
// feed echoes the symbol the client just subscribed to — and that is
// evidence worth surfacing: it did not come from guessing what the field
// might contain, only from noticing it matches something the recording
// already shows was sent.
func collectSentStrings(ss Sessions) map[string]bool {
	out := map[string]bool{}

	collect := func(v jsontext.Value) {
		if len(v) == 0 {
			return
		}

		var any any
		if err := json.Unmarshal(v, &any); err != nil {
			return
		}

		collectStrings(any, out)
	}

	for _, s := range ss {
		for _, f := range s.Frames {
			collect(f.Send)
		}

		collect(s.Unsubscribe)
	}

	return out
}

// collectStrings walks an arbitrary decoded JSON value and adds every string
// it finds, at any depth, to out.
func collectStrings(v any, out map[string]bool) {
	switch v := v.(type) {
	case string:
		out[v] = true
	case []any:
		for _, e := range v {
			collectStrings(e, out)
		}
	case map[string]any:
		for _, e := range v {
			collectStrings(e, out)
		}
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

func detectSchemaBinary(s *asyncapi.Schema, sent map[string]bool) {
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
	s.Description = mergeString(s.Description, describeProtoLayout(layout, sent))
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

// describeProtoLayout renders what consistentProtoLayout found about the data
// itself — not how it was found — for [asyncapi.Schema.Description]: one line
// per field naming its shape (a short string, a number in some range, a
// counter that only grows), then any cross-field relationship the samples
// support (a ratio that holds across every one of them).
func describeProtoLayout(layout []protoFieldSamples, sent map[string]bool) string {
	var b strings.Builder

	b.WriteString("Protobuf-encoded (base64 on the wire). No field is named — " +
		"each line below is what the recorded samples show that field holding.\n")

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
			fmt.Fprintf(&b, ", present in %d of %d samples", f.seenIn, nSamples)
		}

		b.WriteString(": ")
		b.WriteString(describeFieldValues(f, sent))
	}

	if rel := findRatioRelationships(layout); len(rel) > 0 {
		b.WriteString("\n\nAcross every sample:\n")

		for _, r := range rel {
			b.WriteString("  ")
			b.WriteString(r)
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
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

// describeFieldValues renders what one field's own samples show: a short
// string (naming whether it exactly matches something the client itself
// sent), a number and its range or trend, or — for a varint, which protobuf
// uses for both plain and zigzag-encoded signed integers with nothing in the
// wire format itself saying which — both readings when they disagree, since
// only a .proto (or an application that knows what it is talking to) can
// settle that one.
func describeFieldValues(f protoFieldSamples, sent map[string]bool) string {
	switch f.wireType {
	case protoWireBytes:
		return describeBytesField(f, sent)
	case protoWireVarint:
		return describeVarintField(f)
	case protoWireFixed32:
		return describeNumeric(f, func(v protoField) float64 { return float64(v.Float32()) })
	case protoWireFixed64:
		return describeNumeric(f, func(v protoField) float64 { return v.Float64() })
	default:
		return "(unrecognized wire type)"
	}
}

func describeBytesField(f protoFieldSamples, sent map[string]bool) string {
	allUTF8 := true
	allSent := len(f.values) > 0

	strs := make([]string, len(f.values))

	for i, v := range f.values {
		if !utf8.Valid(v.raw) {
			allUTF8 = false
		}

		if !sent[string(v.raw)] {
			allSent = false
		}

		strs[i] = fmt.Sprintf("%q", string(v.raw))
	}

	if !allUTF8 {
		hexes := make([]string, len(f.values))
		for i, v := range f.values {
			hexes[i] = fmt.Sprintf("%x", v.raw)
		}

		return "binary data, e.g. " + strings.Join(dedupLimit(hexes, 3), ", ")
	}

	desc := "a short string, e.g. " + strings.Join(dedupLimit(strs, 3), ", ")

	if allSent {
		desc += " — every value recorded here is one the client itself sent earlier in the session"
	}

	return desc
}

func describeVarintField(f protoFieldSamples) string {
	plain := make([]string, len(f.values))
	zigzag := make([]string, len(f.values))
	monotonic := true
	constant := true

	for i, v := range f.values {
		plain[i] = fmt.Sprintf("%d", v.uvarint)
		zigzag[i] = fmt.Sprintf("%d", zigzagDecode(v.uvarint))

		if i > 0 {
			if v.uvarint < f.values[i-1].uvarint {
				monotonic = false
			}

			if v.uvarint != f.values[i-1].uvarint {
				constant = false
			}
		}
	}

	desc := "a number, e.g. " + strings.Join(dedupLimit(plain, 3), ", ")

	if plain[0] != zigzag[0] {
		desc += fmt.Sprintf(" (protobuf's varint wire type also allows a zigzag-encoded"+
			" signed reading, which this would be: %s — the wire bytes alone do not say which is intended)",
			strings.Join(dedupLimit(zigzag, 3), ", "))
	}

	switch {
	case constant && len(f.values) >= minSamplesForBinaryDetection:
		if v := f.values[0].uvarint; v < 128 {
			desc += fmt.Sprintf(" — the same value (%d) in every sample; a small constant like this is often an enum member or a flag", v)
		} else {
			desc += " — the same value in every sample"
		}
	case monotonic && len(f.values) >= minSamplesForBinaryDetection:
		desc += " — only ever grows from one sample to the next, like a running count"
	}

	return desc
}

func describeNumeric(f protoFieldSamples, asFloat func(protoField) float64) string {
	nums := make([]string, len(f.values))
	monotonic := true

	for i, v := range f.values {
		nums[i] = fmt.Sprintf("%v", asFloat(v))

		if i > 0 && asFloat(v) < asFloat(f.values[i-1]) {
			monotonic = false
		}
	}

	desc := "a number, e.g. " + strings.Join(dedupLimit(nums, 3), ", ")

	if monotonic && len(f.values) >= minSamplesForBinaryDetection && nums[0] != nums[len(nums)-1] {
		desc += " — only ever grows from one sample to the next, like a running count"
	}

	return desc
}

// ratioTolerance is how far a field-pair's ratio may drift, sample to
// sample, and still count as "the same ratio" — wide enough that the last
// digit or two of float32 rounding does not break the match, narrow enough
// that two unrelated numbers are very unlikely to satisfy it by chance.
const ratioTolerance = 0.02

// findRatioRelationships looks for pairs of numeric fields whose ratio holds
// steady across every sample both were present in — a value that only ever
// tracks another one proportionally, the way a monetary change and a percent
// change both track a price. It skips a field that is constant on its own
// (a steady ratio to a constant is not a discovery) and reports each
// qualifying pair once.
func findRatioRelationships(layout []protoFieldSamples) []string {
	type numericField struct {
		number int
		values []float64
	}

	var numeric []numericField

	for _, f := range layout {
		if f.wireType != protoWireFixed32 && f.wireType != protoWireFixed64 {
			continue
		}

		values := make([]float64, len(f.values))
		nonZero, distinct := false, false

		for i, v := range f.values {
			if f.wireType == protoWireFixed32 {
				values[i] = float64(v.Float32())
			} else {
				values[i] = v.Float64()
			}

			if values[i] != 0 {
				nonZero = true
			}

			if i > 0 && values[i] != values[0] {
				distinct = true
			}
		}

		if nonZero && distinct {
			numeric = append(numeric, numericField{f.number, values})
		}
	}

	var out []string

	for i := range numeric {
		for j := range numeric {
			if i >= j || len(numeric[i].values) != len(numeric[j].values) {
				continue
			}

			if ratio, ok := steadyRatio(numeric[i].values, numeric[j].values); ok {
				out = append(out, fmt.Sprintf("field %d / field %d ≈ %.4g in every sample",
					numeric[i].number, numeric[j].number, ratio))
			}
		}
	}

	return out
}

// steadyRatio reports the ratio a/b if it stays within [ratioTolerance] of
// its own mean across every paired sample, and both sides avoid dividing by
// (or near) zero.
func steadyRatio(a, b []float64) (float64, bool) {
	ratios := make([]float64, len(a))

	for i := range a {
		if math.Abs(b[i]) < 1e-9 {
			return 0, false
		}

		ratios[i] = a[i] / b[i]
	}

	min, max := ratios[0], ratios[0]

	for _, r := range ratios[1:] {
		min = math.Min(min, r)
		max = math.Max(max, r)
	}

	mean := (min + max) / 2
	if mean == 0 {
		return 0, false
	}

	if (max-min)/math.Abs(mean) > ratioTolerance {
		return 0, false
	}

	return mean, true
}

// zigzagDecode reverses protobuf's zigzag encoding, the mapping an sint32 or
// sint64 field uses to make small negative numbers as cheap to encode as
// small positive ones: 0,1,2,3,4 → 0,-1,1,-2,2. A varint that is not actually
// zigzag-encoded still decodes under this formula without erroring — it just
// produces a different, equally "valid-looking" number — which is exactly
// why the wire bytes alone cannot say which reading (plain or zigzag) the
// schema intended.
func zigzagDecode(u uint64) int64 {
	return int64(u>>1) ^ -int64(u&1)
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
