package enrich

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"

	"github.com/MarkRosemaker/asyncapi"
	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// validateProtoSchemas checks every field whose ContentSchema declares a
// protobuf message (schemaFormat application/vnd.google.protobuf, either
// version) against that field's own recorded examples, and reports every
// field-number mismatch it finds as one error. A maintainer who has pasted
// the real .proto in gets an early, specific signal the moment a recording
// no longer matches it — not a schema that silently drifted out of sync with
// what the API actually sends.
//
// This is deliberately narrower than what [detectBinaryEncodings] does on
// its own: that package has no .proto to check against and only reports
// structural shape (field number, wire type). Here, a .proto is given, so
// "does the recording match it" is an answerable question, not a heuristic.
func validateProtoSchemas(doc *asyncapi.Document) error {
	names := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}

	sort.Strings(names)

	var problems []string

	for _, name := range names {
		walkSchemaNamed(name, doc.Components.Schemas[name].Value.Schema, func(path string, s *asyncapi.Schema) {
			if p := validateSchemaProtoContent(path, s); p != "" {
				problems = append(problems, p)
			}
		})
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("recorded data does not match the declared protobuf schema:\n\n%s",
		strings.Join(problems, "\n\n"))
}

// walkSchemaNamed calls fn on s and on every schema reachable from it, each
// time with the dotted path that reached it (e.g. "Pricing.message") — the
// context [walkSchema] does not carry, and the one thing that makes a
// mismatch report useful instead of just "some field, somewhere".
func walkSchemaNamed(path string, s *asyncapi.Schema, fn func(path string, s *asyncapi.Schema)) {
	if s == nil {
		return
	}

	fn(path, s)

	propNames := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		propNames = append(propNames, name)
	}

	sort.Strings(propNames)

	for _, name := range propNames {
		walkSchemaNamed(path+"."+name, s.Properties[name].Value.Schema, fn)
	}

	if s.Items != nil {
		walkSchemaNamed(path+"[]", s.Items.Value.Schema, fn)
	}

	if s.AdditionalProperties != nil {
		walkSchemaNamed(path+".*", s.AdditionalProperties.Value.Schema, fn)
	}
}

// validateSchemaProtoContent checks s's own examples against s.ContentSchema,
// if it declares a protobuf message, and returns a description of every
// mismatch found (empty if there is nothing to check, or nothing wrong).
func validateSchemaProtoContent(path string, s *asyncapi.Schema) string {
	if s.ContentSchema == nil || s.ContentSchema.Value == nil {
		return ""
	}

	cs := s.ContentSchema.Value
	if !isProtobufFormat(cs.SchemaFormat) {
		return ""
	}

	msgDesc, err := compileProtoMessage(cs)
	if err != nil {
		return fmt.Sprintf("contentSchema: %v", err)
	}

	expected := expectedWireTypes(msgDesc)

	var mismatches []string

	for _, ex := range s.Examples {
		var str string
		if err := json.Unmarshal(ex, &str); err != nil {
			continue // not a plain string; contentEncoding: base64 would not apply
		}

		data, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			continue
		}

		fields, err := parseProtoWire(data)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf(
				"  example %q does not parse as protobuf at all: %v", str, err,
			))

			continue
		}

		seen := map[int]bool{}

		for _, f := range fields {
			if seen[f.Number] {
				continue // report each field number once per example
			}

			seen[f.Number] = true

			want, ok := expected[f.Number]
			if !ok {
				continue // proto does not declare this field number; not a conflict
			}

			if want.wireType != f.WireType {
				mismatches = append(mismatches, describeWireMismatch(str, want, f.WireType))
			}
		}
	}

	if len(mismatches) == 0 {
		return ""
	}

	return fmt.Sprintf("%s (declared as %s, %s):\n%s",
		path, msgDesc.FullName(), cs.SchemaFormat, strings.Join(mismatches, "\n"))
}

// describeWireMismatch renders one field's conflict, including which kinds
// the wire type actually recorded would be consistent with — the closest
// thing to a suggestion this package can make without knowing what the field
// is actually supposed to be.
func describeWireMismatch(example string, want expectedField, observed int) string {
	return fmt.Sprintf(
		"  field %d (%q, declared as %s, wire type %s) — example %q instead shows wire type %s, "+
			"which %s. The recorded traffic does not look like this message.",
		want.number, want.name, want.kind, wireTypeName(want.wireType),
		example, wireTypeName(observed), kindsForWireType(observed),
	)
}

// isProtobufFormat reports whether f names either protobuf schema format the
// AsyncAPI specification lists.
func isProtobufFormat(f asyncapi.SchemaFormat) bool {
	return f == asyncapi.SchemaFormatProtobuf2 || f == asyncapi.SchemaFormatProtobuf3
}

// protoMessageExtension names which message in a contentSchema's .proto text
// to validate against — needed only when the .proto defines more than one
// top-level message, since nothing else says which one applies here.
type protoMessageExtension struct {
	Message string `json:"x-protobuf-message"`
}

// compileProtoMessage compiles cs.Raw (the raw .proto source [AnySchema.Raw]
// holds for a non-JSON format) and returns the message descriptor to
// validate against: the one named by the x-protobuf-message extension, or
// the file's only top-level message if it has just one.
func compileProtoMessage(cs *asyncapi.AnySchema) (protoreflect.MessageDescriptor, error) {
	var source string
	if err := json.Unmarshal(cs.Raw, &source); err != nil {
		return nil, fmt.Errorf("schema is not a string (expected raw .proto source): %w", err)
	}

	const filename = "contentSchema.proto"

	compiler := protocompile.Compiler{
		Resolver: &protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{filename: source}),
		},
	}

	files, err := compiler.Compile(context.Background(), filename)
	if err != nil {
		return nil, fmt.Errorf("compiling .proto: %w", err)
	}

	messages := files[0].Messages()

	var ext protoMessageExtension
	if len(cs.Extensions) > 0 {
		_ = json.Unmarshal(cs.Extensions, &ext) // best effort; missing key just leaves it empty
	}

	if ext.Message != "" {
		msg := messages.ByName(protoreflect.Name(ext.Message))
		if msg == nil {
			return nil, fmt.Errorf("x-protobuf-message names %q, but the .proto defines no such message", ext.Message)
		}

		return msg, nil
	}

	switch messages.Len() {
	case 0:
		return nil, fmt.Errorf(".proto defines no messages")
	case 1:
		return messages.Get(0), nil
	default:
		names := make([]string, messages.Len())
		for i := range messages.Len() {
			names[i] = string(messages.Get(i).Name())
		}

		return nil, fmt.Errorf(".proto defines %d messages (%s); "+
			`set "x-protobuf-message" on the contentSchema to say which one applies here`,
			messages.Len(), strings.Join(names, ", "))
	}
}

// expectedField is what a .proto's own field declaration says a field number
// should look like on the wire.
type expectedField struct {
	number   int
	name     string
	kind     protoreflect.Kind
	wireType int
}

// expectedWireTypes maps every field a message descriptor declares to the
// wire type its kind requires, per the Protocol Buffers encoding spec. A
// field whose kind this package does not map (Group — deprecated since
// proto2 and not something proto3 sources ever declare) is left out, since
// there is nothing to validate against without one.
func expectedWireTypes(msg protoreflect.MessageDescriptor) map[int]expectedField {
	out := make(map[int]expectedField, msg.Fields().Len())

	fields := msg.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)

		wt, ok := wireTypeForKind(f.Kind())
		if !ok {
			continue
		}

		out[int(f.Number())] = expectedField{
			number:   int(f.Number()),
			name:     string(f.Name()),
			kind:     f.Kind(),
			wireType: wt,
		}
	}

	return out
}

// wireTypeForKind maps a protobuf field kind to the wire type it is encoded
// with, per https://protobuf.dev/programming-guides/encoding/#structure.
// This mapping is what makes checking a recording against a .proto possible
// at all: the wire bytes only ever carry a wire type, never a kind, so this
// is the other half of the comparison [parseProtoWire] can't supply on its
// own.
func wireTypeForKind(k protoreflect.Kind) (int, bool) {
	switch k {
	case protoreflect.BoolKind, protoreflect.EnumKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind:
		return protoWireVarint, true
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return protoWireFixed64, true
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return protoWireBytes, true
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return protoWireFixed32, true
	default:
		return 0, false
	}
}

// wireTypeName renders a protobuf wire type the way a person reading a
// mismatch report needs it: not just the number, but what it means.
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

// allKinds is every scalar/message/enum kind protoreflect defines, used to
// enumerate "which kinds use this wire type" for [kindsForWireType]. Listed
// explicitly rather than ranged over: Kind's numeric values are not assigned
// in wire-type order (GroupKind is 10, but MessageKind is 11 and BytesKind is
// 12), so a numeric range silently skips members depending on where it stops.
var allKinds = []protoreflect.Kind{
	protoreflect.BoolKind, protoreflect.EnumKind,
	protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
	protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind,
	protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind,
	protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind, protoreflect.GroupKind,
	protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind,
}

// kindsForWireType lists, in prose, which proto kinds a wire type is used
// for — the closest thing to a suggestion this package can offer once it
// knows a field does not match what was declared.
func kindsForWireType(wt int) string {
	var kinds []string

	for _, k := range allKinds {
		if got, ok := wireTypeForKind(k); ok && got == wt {
			kinds = append(kinds, k.String())
		}
	}

	sort.Strings(kinds)

	return "protobuf uses for: " + strings.Join(kinds, ", ")
}
