package enrich_test

import (
	"strings"
	"testing"

	enrich "github.com/MarkRosemaker/asyncapi-enrich"
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

// TestDetectBinaryDataDescriptions checks the four evidence-based claims the
// detector is willing to make about a field's data, each tied to a fact the
// three crafted samples below actually demonstrate rather than a guess:
//
//   - field 1 (bytes "BTC") exactly matches a value the session's one send
//     frame carries, so its description says so.
//   - field 3 (varint, 10 → 12 → 15) only ever grows, so it reads as a
//     running count.
//   - field 4 (varint, 9 in every sample) is small and constant, so it reads
//     as a possible enum member or flag.
//   - field 2 (fixed32 price) and field 5 (fixed32, exactly price/100 in
//     every sample) hold a steady ratio, so that relationship is reported.
//
// The samples are built by hand from the wire format spec (see
// TestDetectBinaryConsistentProtobufIsMarked), not captured from anywhere.
func TestDetectBinaryDataDescriptions(t *testing.T) {
	doc := enrich.NewDocument()
	ss := enrich.Sessions{{
		URI: "ws://example.invalid",
		Frames: []*enrich.Frame{
			{Send: []byte(`{"subscribe":["BTC"]}`)},
			{Receive: []byte(`{"type":"pricing","message":"CgNCVEMVAADIQhgKIAktAACAPw=="}`)},
			{Receive: []byte(`{"type":"pricing","message":"CgNCVEMVAADKQhgMIAktrkeBPw=="}`)},
			{Receive: []byte(`{"type":"pricing","message":"CgNCVEMVAADMQhgPIAktXI+CPw=="}`)},
		},
	}}

	if err := enrich.Enrich(doc, ss); err != nil {
		t.Fatalf("enriching: %v", err)
	}

	ref, ok := doc.Components.Schemas["Pricing"]
	if !ok {
		t.Fatalf("no Pricing schema; have %v", schemaNames(doc))
	}

	message := ref.Value.Schema.Properties["message"].Value.Schema

	if message.ContentEncoding != "base64" {
		t.Fatalf("message.contentEncoding: got %q, want %q", message.ContentEncoding, "base64")
	}

	desc := message.Description

	checks := []struct {
		name string
		want string
	}{
		{"sent-string match", "the client itself sent"},
		{"running count", "field 3 (varint): a number, e.g. 10, 12, 15"},
		{"enum/flag hint", "enum member or a flag"},
		{"ratio relationship", "field 2 / field 5"},
	}

	for _, c := range checks {
		if !strings.Contains(desc, c.want) {
			t.Errorf("%s: description missing %q:\n%s", c.name, c.want, desc)
		}
	}
}

// TestDetectBinaryZigzagAmbiguityIsSurfaced checks that a varint field whose
// plain and zigzag-decoded readings disagree gets both readings reported,
// rather than the detector silently picking one — nothing in protobuf's wire
// format says which encoding a varint field uses, so this package does not
// pretend otherwise. Field 1 here is a varint whose zigzag reading (-1)
// differs from its plain one (1).
func TestDetectBinaryZigzagAmbiguityIsSurfaced(t *testing.T) {
	s := merge(t, "Weird",
		`{"type":"weird","v":"CAE="}`,
		`{"type":"weird","v":"CAI="}`,
	)

	v := s.Properties["v"].Value.Schema
	if v.ContentEncoding != "base64" {
		t.Fatalf("v.contentEncoding: got %q, want %q", v.ContentEncoding, "base64")
	}

	if !strings.Contains(v.Description, "zigzag") {
		t.Errorf("v.description: missing the zigzag ambiguity note:\n%s", v.Description)
	}
}
