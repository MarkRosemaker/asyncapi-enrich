package enrich

import (
	"encoding/json/jsontext"
	"fmt"
	"slices"

	"github.com/MarkRosemaker/asyncapi"
)

// mergeSchema merges b into a in place, so that a describes every shape b did
// as well as every shape it already described.
//
// This is deliberately narrower than [github.com/MarkRosemaker/openapi-merge]:
// that package reconciles years of OpenAPI 3.0 schemas, which have no way to
// say a value may be more than one type and so had to invent workarounds —
// oneOf branches for a date that is sometimes a string and sometimes a unix
// timestamp, a placeholder object standing in for a JSON null. An AsyncAPI
// schema is JSON Schema, whose `type` keyword is natively a list, so the
// corresponding case here — a field that is sometimes null, sometimes not — is
// just a type union, no workaround needed.
//
// What mergeSchema exists to get right is the question the whole tool was
// built to answer: which fields are actually required. A key present in every
// sample merged into a schema stays required; a key present in only some of
// them does not — because that is, definitionally, what "optional" means, and
// no single recording can tell the two apart on its own.
func mergeSchema(a, b *asyncapi.Schema) error {
	a.Title = mergeString(a.Title, b.Title)
	a.Description = mergeString(a.Description, b.Description)

	aNull, bNull := isNullOnly(a), isNullOnly(b)

	a.Type = unionTypes(a.Type, b.Type)

	switch {
	case aNull && bNull:
		return nil // both null; nothing more to learn
	case aNull:
		// a had no structure of its own to merge into b's — adopt b's shape
		// wholesale, keeping the union type so the null that did occur is
		// still on record.
		tp := a.Type
		*a = *b
		a.Type = tp

		return nil
	case bNull:
		// b confirms null occurs but contributes no new shape; a already has
		// the real one.
		return nil
	}

	switch {
	case a.Type.Contains(asyncapi.TypeObject) && a.Type.Contains(asyncapi.TypeArray):
		return fmt.Errorf("a schema cannot be both object- and array-shaped: %s vs %s", a.Type, b.Type)
	case a.Type.Contains(asyncapi.TypeObject):
		return mergeObject(a, b)
	case a.Type.Contains(asyncapi.TypeArray):
		return mergeArray(a, b)
	default:
		mergeScalar(a, b)

		return nil
	}
}

// isNullOnly reports whether a schema's only type is null — the schema of a
// literal JSON null, which says nothing about shape yet.
func isNullOnly(s *asyncapi.Schema) bool {
	return len(s.Type) == 1 && s.Type[0] == asyncapi.TypeNull
}

// unionTypes returns every type either a or b can be, deduplicated. Integer is
// a subset of number, so once both are seen the pair collapses to number
// alone — carrying both would say nothing "number" does not already say. Null,
// if present, is kept last so a schema reads as its real type first, e.g.
// ["string", "null"] rather than ["null", "string"].
func unionTypes(a, b asyncapi.DataTypes) asyncapi.DataTypes {
	out := make(asyncapi.DataTypes, 0, len(a)+len(b))

	for _, d := range a {
		if !out.Contains(d) {
			out = append(out, d)
		}
	}

	for _, d := range b {
		if !out.Contains(d) {
			out = append(out, d)
		}
	}

	if out.Contains(asyncapi.TypeInteger) && out.Contains(asyncapi.TypeNumber) {
		out = slices.DeleteFunc(out, func(d asyncapi.DataType) bool { return d == asyncapi.TypeInteger })
	}

	if i := slices.Index(out, asyncapi.TypeNull); i != -1 && i != len(out)-1 {
		out = append(slices.Delete(out, i, i+1), asyncapi.TypeNull)
	}

	return out
}

// mergeObject merges b's properties into a's.
//
// A key present in both is merged recursively. A key present in only one of
// them is kept as-is — one sample not carrying an optional field does not
// change what that field looks like when it is present. Required becomes the
// intersection of the two: a key required on both sides stays required, and
// dropping out of Required is exactly how a field earns "optional" here, since
// every schema starts out requiring everything a single sample happened to
// carry (see decodeObjectSchema).
func mergeObject(a, b *asyncapi.Schema) error {
	if a.AdditionalProperties != nil || b.AdditionalProperties != nil {
		switch {
		case a.AdditionalProperties != nil && b.AdditionalProperties != nil:
			return mergeSchema(a.AdditionalProperties.Value.Schema, b.AdditionalProperties.Value.Schema)
		case b.AdditionalProperties != nil:
			a.AdditionalProperties = b.AdditionalProperties
		}

		return nil
	}

	if a.Properties == nil {
		a.Properties = asyncapi.Schemas{}
	}

	for key, bProp := range b.Properties {
		aProp, ok := a.Properties[key]
		if !ok {
			a.Properties[key] = bProp

			continue
		}

		if err := mergeSchema(aProp.Value.Schema, bProp.Value.Schema); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}

	bRequired := make(map[string]bool, len(b.Required))
	for _, k := range b.Required {
		bRequired[k] = true
	}

	kept := a.Required[:0]

	for _, k := range a.Required {
		if bRequired[k] {
			kept = append(kept, k)
		}
	}

	a.Required = kept

	return nil
}

// mergeArray merges b's item schema into a's.
func mergeArray(a, b *asyncapi.Schema) error {
	switch {
	case a.Items == nil && b.Items == nil:
		return nil
	case a.Items == nil:
		a.Items = b.Items
	case b.Items != nil:
		if err := mergeSchema(a.Items.Value.Schema, b.Items.Value.Schema); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}

	return nil
}

// maxExamples caps how many distinct examples a scalar schema keeps. Beyond a
// handful, another example teaches nothing a reader does not already know from
// the type and format.
const maxExamples = 3

// mergeScalar reconciles the format and examples of two schemas of the same
// scalar type.
func mergeScalar(a, b *asyncapi.Schema) {
	// If one side conforms to a format the other does not, the format cannot
	// be guaranteed for the type as a whole, so it is dropped rather than kept
	// as a claim that would sometimes be wrong.
	if a.Format != b.Format {
		a.Format = ""
	}

	for _, ex := range b.Examples {
		if len(a.Examples) >= maxExamples {
			break
		}

		if !slices.ContainsFunc(a.Examples, func(have jsontext.Value) bool {
			return string(have) == string(ex)
		}) {
			a.Examples = append(a.Examples, ex)
		}
	}
}

// mergeString returns whichever of a and b is non-empty, preferring a when
// both are set.
func mergeString(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
