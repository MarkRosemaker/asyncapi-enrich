package enrich

import (
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/MarkRosemaker/asyncapi"
)

// detectBinaryEncodings walks every schema reachable from doc.Components.Schemas
// and marks a string field ContentEncoding "base64" when its recorded
// examples are all base64 and decode to a self-consistent raw protobuf byte
// stream — see [looksLikeProtobuf].
//
// This exists because a WebSocket API is free to put anything inside a JSON
// string, and at least one real one (Yahoo Finance's pricing feed) puts
// base64-encoded protobuf there: without this, that field would infer as a
// plain string, and a generator downstream would have no signal that
// json.Unmarshal is the wrong way to read it. What it decides is exactly
// that one bit — is this base64, does it decode to protobuf — and no more:
// describing what the decoded bytes mean is the maintainer's job, not
// something a recording proves on its own.
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

	if looksLikeProtobuf(samples) {
		s.ContentEncoding = "base64"
	}
}

// looksLikeProtobuf reports whether every sample parses cleanly as raw
// protobuf wire format (see [parseProtoWire]) and every field number that
// appears in more than one sample carries the same wire type each time it
// does — protobuf's tag byte would not vary the wire type of the same field
// number between messages of the same type, so a mismatch means these bytes
// are not what they appear to be.
func looksLikeProtobuf(samples [][]byte) bool {
	wireTypeByNumber := map[int]int{}

	for _, data := range samples {
		fields, err := parseProtoWire(data)
		if err != nil || len(fields) == 0 {
			return false
		}

		for _, f := range fields {
			if wt, ok := wireTypeByNumber[f.Number]; ok {
				if wt != f.WireType {
					return false
				}
			} else {
				wireTypeByNumber[f.Number] = f.WireType
			}
		}
	}

	return true
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

		switch wireType {
		case protoWireVarint:
			_, n, err := readVarint(data[i:])
			if err != nil {
				return nil, fmt.Errorf("varint at byte %d: %w", i, err)
			}

			i += n

		case protoWireFixed64:
			if i+8 > len(data) {
				return nil, fmt.Errorf("fixed64 at byte %d: truncated", i)
			}

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

			i += int(length)

		case protoWireFixed32:
			if i+4 > len(data) {
				return nil, fmt.Errorf("fixed32 at byte %d: truncated", i)
			}

			i += 4

		default:
			return nil, fmt.Errorf("wire type %d at byte %d: not a protobuf wire type", wireType, i)
		}

		out = append(out, protoField{Number: number, WireType: wireType})
	}

	return out, nil
}

// readVarint decodes a base-128 varint from the start of data, per protobuf's
// encoding, and reports how many bytes it took. It caps at 10 bytes — the
// most a 64-bit varint ever needs — so non-protobuf bytes cannot spin it into
// reading past a sane bound.
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
