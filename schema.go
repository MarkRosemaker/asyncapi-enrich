package enrich

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/MarkRosemaker/asyncapi"
	apitypes "github.com/go-api-libs/types"
	"github.com/google/uuid"
)

// The string formats JSON Schema defines that [asyncapi.Format] does not name as
// constants — it only names the numeric and binary ones. These are used as-is:
// they are standard JSON Schema vocabulary, so a name asyncapi.Format leaves
// open is still the correct thing to write.
const (
	formatUUID  asyncapi.Format = "uuid"
	formatURI   asyncapi.Format = "uri"
	formatEmail asyncapi.Format = "email"
	formatIPv4  asyncapi.Format = "ipv4"
	formatIPv6  asyncapi.Format = "ipv6"
)

// newSchemaFromJSON infers an AsyncAPI schema from a JSON-encoded value.
func newSchemaFromJSON(data jsontext.Value) (*asyncapi.Schema, error) {
	return decodeSchema(jsontext.NewDecoder(bytes.NewReader(data)))
}

func decodeSchema(dec *jsontext.Decoder) (*asyncapi.Schema, error) {
	v, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}

	switch v.Kind() {
	case '"':
		s := &asyncapi.Schema{
			Type:   asyncapi.DataTypes{asyncapi.TypeString},
			Format: stringFormat(v.String()),
		}

		// A masked value makes a poor example: it says nothing the inferred
		// format does not already say, and reads as though the API returns
		// asterisks. Masking is shape-preserving, so the format survives
		// without it.
		if v.String() != Replacement {
			s.Examples = []jsontext.Value{quote(v.String())}
		}

		return s, nil

	case '0':
		str := v.String()
		if _, err := strconv.Atoi(str); err == nil {
			return &asyncapi.Schema{
				Type:     asyncapi.DataTypes{asyncapi.TypeInteger},
				Examples: []jsontext.Value{jsontext.Value(str)},
			}, nil
		}

		return &asyncapi.Schema{
			Type:     asyncapi.DataTypes{asyncapi.TypeNumber},
			Format:   asyncapi.FormatDouble,
			Examples: []jsontext.Value{jsontext.Value(str)},
		}, nil

	case 't', 'f':
		return &asyncapi.Schema{Type: asyncapi.DataTypes{asyncapi.TypeBoolean}}, nil

	case 'n':
		// Unlike OpenAPI 3.x, an AsyncAPI schema's type is a real JSON Schema
		// DataTypes — a list, not a single value — so null has a type of its
		// own rather than needing an object-shaped placeholder to stand in for
		// "unknown, refine later". Merging a null schema with a typed one below
		// is what turns this into the honest union, e.g. ["string", "null"].
		return &asyncapi.Schema{Type: asyncapi.DataTypes{asyncapi.TypeNull}}, nil

	case '{':
		return decodeObjectSchema(dec)

	case '[':
		return decodeArraySchema(dec)

	default:
		return nil, fmt.Errorf("unexpected token type %s", v.Kind())
	}
}

func decodeObjectSchema(dec *jsontext.Decoder) (*asyncapi.Schema, error) {
	type kv struct {
		key    string
		schema *asyncapi.Schema
	}

	var pairs []kv

	for dec.PeekKind() != '}' {
		keyTok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}

		if keyTok.Kind() != '"' {
			return nil, fmt.Errorf("expected string key, got %s", keyTok.Kind())
		}

		key := keyTok.String()

		propSchema, err := decodeSchema(dec)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", key, err)
		}

		// A masked number is indistinguishable from a real one by value, so
		// drop the example by key instead. These keys hold credentials, which
		// have no business appearing as examples either way.
		if NewMasker().masks(key) {
			propSchema.Examples = nil
		}

		pairs = append(pairs, kv{key, propSchema})
	}

	if _, err := dec.ReadToken(); err != nil { // consume '}'
		return nil, err
	}

	if len(pairs) == 0 {
		return &asyncapi.Schema{Type: asyncapi.DataTypes{asyncapi.TypeObject}}, nil
	}

	// If every key is a stringified non-negative integer, the object is a
	// numeric-keyed map (e.g. {"0":{...},"1":{...}}). Model it with
	// additionalProperties so the key pattern is explicit and the schema stays
	// compact regardless of how many entries are present.
	allNumeric := true

	for _, p := range pairs {
		if !isNumericKey(p.key) {
			allNumeric = false

			break
		}
	}

	if allNumeric {
		var valueSchema *asyncapi.Schema

		for _, p := range pairs {
			if valueSchema == nil {
				valueSchema = p.schema

				continue
			}

			if err := mergeSchema(valueSchema, p.schema); err != nil {
				return nil, fmt.Errorf("merging additionalProperties value: %w", err)
			}
		}

		return &asyncapi.Schema{
			Type:                 asyncapi.DataTypes{asyncapi.TypeObject},
			AdditionalProperties: &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: valueSchema}},
		}, nil
	}

	// Named properties — build the schema as before. Every key seen in this one
	// object is required for now; a field only some messages carry becomes
	// optional the moment this schema is merged with one that lacks it — see
	// mergeSchema.
	s := &asyncapi.Schema{
		Type:       asyncapi.DataTypes{asyncapi.TypeObject},
		Properties: asyncapi.Schemas{},
	}

	for _, p := range pairs {
		s.Properties[p.key] = &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: p.schema}}
		s.Required = append(s.Required, p.key)
	}

	return s, nil
}

// isNumericKey reports whether s consists entirely of ASCII digits.
func isNumericKey(s string) bool {
	if len(s) == 0 {
		return false
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

func decodeArraySchema(dec *jsontext.Decoder) (*asyncapi.Schema, error) {
	s := &asyncapi.Schema{Type: asyncapi.DataTypes{asyncapi.TypeArray}}

	var itemSchema *asyncapi.Schema

	for dec.PeekKind() != ']' {
		elem, err := decodeSchema(dec)
		if err != nil {
			return nil, err
		}

		if itemSchema == nil {
			itemSchema = elem
		} else if err := mergeSchema(itemSchema, elem); err != nil {
			return nil, fmt.Errorf("merging array items: %w", err)
		}
	}

	if _, err := dec.ReadToken(); err != nil { // consume ']'
		return nil, err
	}

	if itemSchema == nil {
		// An empty array says nothing about what it holds; leave items unset
		// rather than inventing a shape for it.
		return s, nil
	}

	s.Items = &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: itemSchema}}

	return s, nil
}

// stringFormat detects the format of a string value.
// It tries in order: UUID, URI, email, date-time (RFC3339), IPv4/IPv6.
func stringFormat(s string) asyncapi.Format {
	if uuid.Validate(s) == nil {
		return formatUUID
	}

	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return formatURI
	}

	if apitypes.Email(s).Validate() == nil {
		return formatEmail
	}

	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return asyncapi.FormatDateTime
	}

	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return formatIPv4
		}

		return formatIPv6
	}

	return ""
}
