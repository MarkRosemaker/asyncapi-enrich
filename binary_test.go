package enrich_test

import (
	"strings"
	"testing"
)

// TestDetectBinaryConsistentProtobufIsMarked checks the positive case: three
// samples that are valid base64 and whose decoded bytes agree, field number
// for field number, on wire type — the two structural facts this package
// checks for and the only ones it is willing to call evidence.
//
// The three base64 strings below decode to a two-field protobuf message
// (field 1 varint, field 2 length-delimited bytes "AAPL"/"MSFT"/"NVDA") built
// by hand from the wire format spec, not captured from anywhere — this test
// only needs to know the detector's own rules hold, not reproduce Yahoo's feed.
func TestDetectBinaryConsistentProtobufIsMarked(t *testing.T) {
	s := merge(t, "Pricing",
		`{"type":"pricing","message":"CAoSBEFBUEw="}`,
		`{"type":"pricing","message":"CBQSBE1TRlQ="}`,
		`{"type":"pricing","message":"CB4SBE5WREE="}`,
	)

	message := s.Properties["message"].Value.Schema

	if message.ContentEncoding != "base64" {
		t.Fatalf("message.contentEncoding: got %q, want %q", message.ContentEncoding, "base64")
	}

	if message.Description == "" {
		t.Error("message.description: got empty, want a field-by-field breakdown")
	}

	if !strings.Contains(message.Description, "field 1 (varint)") {
		t.Errorf("message.description: missing field 1 breakdown:\n%s", message.Description)
	}

	if !strings.Contains(message.Description, "field 2 (length-delimited)") {
		t.Errorf("message.description: missing field 2 breakdown:\n%s", message.Description)
	}
}

// TestDetectBinaryNonProtobufBase64IsLeftAlone checks that valid base64 whose
// decoded bytes do not parse as protobuf — the overwhelming majority of
// base64 that shows up in a real API — is left as a plain string. Getting
// this wrong (calling base64 that is not protobuf "protobuf") is the failure
// mode this package cares most about avoiding, since it would misinform
// whatever reads the schema next.
func TestDetectBinaryNonProtobufBase64IsLeftAlone(t *testing.T) {
	s := merge(t, "Blob",
		`{"type":"blob","data":"/xUJDaiB0BQdUUgA4BrvpjS4vEA="}`,
		`{"type":"blob","data":"8bHV7uqYYpXeEbIRQdiTUSzqu/I="}`,
		`{"type":"blob","data":"VGhpcyBpcyBqdXN0IHBsYWluIHRleHQsIG5vdCBwcm90b2J1Zi4="}`,
	)

	data := s.Properties["data"].Value.Schema

	if data.ContentEncoding != "" {
		t.Errorf("data.contentEncoding: got %q, want empty", data.ContentEncoding)
	}
}

// TestDetectBinaryPlainStringIsLeftAlone checks the ordinary case: a string
// field that is not base64 at all is untouched.
func TestDetectBinaryPlainStringIsLeftAlone(t *testing.T) {
	s := merge(t, "Notice",
		`{"type":"notice","text":"market closed"}`,
		`{"type":"notice","text":"market open"}`,
	)

	text := s.Properties["text"].Value.Schema

	if text.ContentEncoding != "" {
		t.Errorf("text.contentEncoding: got %q, want empty", text.ContentEncoding)
	}
}

// TestDetectBinarySingleSampleIsNotEnough checks that one example, however
// cleanly it parses as protobuf, is not enough evidence on its own — a
// single-sample coincidence is more likely than this package treating it as
// proven. "CAoSBEFBUEw=" is the exact payload TestDetectBinaryConsistentProtobufIsMarked
// confirms parses as protobuf; recorded once, it should not be marked.
func TestDetectBinarySingleSampleIsNotEnough(t *testing.T) {
	s := merge(t, "Pricing", `{"type":"pricing","message":"CAoSBEFBUEw="}`)

	message := s.Properties["message"].Value.Schema

	if message.ContentEncoding != "" {
		t.Errorf("message.contentEncoding: got %q, want empty (only one sample recorded)", message.ContentEncoding)
	}
}

// TestDetectBinaryInconsistentWireTypeIsLeftAlone checks that a field number
// carrying a different wire type from one sample to the next — which a real
// protobuf message of one type never does — is treated as disproof, not
// papered over.
func TestDetectBinaryInconsistentWireTypeIsLeftAlone(t *testing.T) {
	// field 1 as varint (value 5), then field 1 as length-delimited ("oops") —
	// same field number, incompatible wire types.
	s := merge(t, "Weird",
		`{"type":"weird","v":"CAU="}`,
		`{"type":"weird","v":"CgRvb3Bz"}`,
	)

	v := s.Properties["v"].Value.Schema

	if v.ContentEncoding != "" {
		t.Errorf("v.contentEncoding: got %q, want empty (inconsistent wire types)", v.ContentEncoding)
	}
}
