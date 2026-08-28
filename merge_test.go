package enrich_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// merge is a small helper: enrich two one-message sessions and return the
// resulting schema, so each test below states only the two payloads and the
// schema they should produce together — the point being tested, not the
// document scaffolding around it.
func merge(t *testing.T, schemaName string, payloads ...string) *asyncapi.Schema {
	t.Helper()

	frames := make([]jsontext.Value, len(payloads))
	for i, p := range payloads {
		frames[i] = jsontext.Value(p)
	}

	doc := enrich.NewDocument()
	ss := enrich.Sessions{{
		URI: "ws://example.invalid",
	}}

	for _, f := range frames {
		ss[0].Frames = append(ss[0].Frames, &enrich.Frame{Receive: f})
	}

	if err := enrich.Enrich(doc, ss); err != nil {
		t.Fatalf("enriching: %v", err)
	}

	ref, ok := doc.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("no schema named %q; have %v", schemaName, schemaNames(doc))
	}

	return ref.Value.Schema
}

func schemaNames(doc *asyncapi.Document) []string {
	names := make([]string, 0, len(doc.Components.Schemas))
	for k := range doc.Components.Schemas {
		names = append(names, k)
	}

	return names
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	return string(b)
}

// TestMergeRequiredIsIntersection is the property the whole tool exists to
// get right: a field present in every sample is required; a field present in
// only some of them is not.
func TestMergeRequiredIsIntersection(t *testing.T) {
	s := merge(t, "Trade",
		`{"type":"trade","s":"AAPL","p":190.5}`,
		`{"type":"trade","s":"AAPL"}`, // no "p" this time
	)

	if got := mustJSON(t, s.Required); got != `["type","s"]` {
		t.Errorf("required: got %s, want [\"type\",\"s\"]", got)
	}

	if _, ok := s.Properties["p"]; !ok {
		t.Error(`"p" should still be a known property, just not required`)
	}
}

// TestMergeSometimesNullBecomesUnion checks that a field seen as both a real
// value and null ends up typed as both — the native JSON Schema way to say
// "optional in value, not just in presence" — rather than losing one side.
func TestMergeSometimesNullBecomesUnion(t *testing.T) {
	s := merge(t, "Quote",
		`{"type":"quote","price":190.5}`,
		`{"type":"quote","price":null}`,
	)

	price := s.Properties["price"].Value.Schema

	if got := mustJSON(t, price.Type); got != `["number","null"]` {
		t.Errorf("price.type: got %s, want [\"number\",\"null\"]", got)
	}
}

// TestMergeIntegerAndNumberBecomeNumber checks that seeing both an integer and
// a float for the same field promotes it to number rather than keeping a type
// list that says nothing "number" alone does not already say.
func TestMergeIntegerAndNumberBecomeNumber(t *testing.T) {
	s := merge(t, "Quote",
		`{"type":"quote","price":190}`,
		`{"type":"quote","price":190.5}`,
	)

	price := s.Properties["price"].Value.Schema

	if got := mustJSON(t, price.Type); got != `"number"` {
		t.Errorf("price.type: got %s, want \"number\"", got)
	}
}

// TestMergeExamplesAccumulateAndCap checks that distinct examples accumulate
// across samples, are deduplicated, and stop growing once there are enough to
// be useful.
func TestMergeExamplesAccumulateAndCap(t *testing.T) {
	s := merge(t, "Quote",
		`{"type":"quote","symbol":"AAPL"}`,
		`{"type":"quote","symbol":"AAPL"}`, // a repeat, should not double up
		`{"type":"quote","symbol":"MSFT"}`,
		`{"type":"quote","symbol":"NVDA"}`,
		`{"type":"quote","symbol":"TSLA"}`, // beyond the cap, should not appear
	)

	symbol := s.Properties["symbol"].Value.Schema

	if got := mustJSON(t, symbol.Examples); got != `["AAPL","MSFT","NVDA"]` {
		t.Errorf("symbol.examples: got %s, want [\"AAPL\",\"MSFT\",\"NVDA\"]", got)
	}
}

// TestMergeArrayItems checks that the schema of an array's items reflects
// every shape seen across every sample, not just the first one.
func TestMergeArrayItems(t *testing.T) {
	s := merge(t, "Trade",
		`{"type":"trade","c":["1"]}`,
		`{"type":"trade","c":["1","8"]}`,
	)

	c := s.Properties["c"].Value.Schema
	if !c.Type.Contains(asyncapi.TypeArray) {
		t.Fatalf("c.type: got %s, want array", c.Type)
	}

	items := c.Items.Value.Schema

	if got := mustJSON(t, items.Examples); got != `["1","8"]` {
		t.Errorf("c.items.examples: got %s, want [\"1\",\"8\"]", got)
	}
}

// TestMergeNumericKeyedMap checks that an object whose keys are all
// stringified integers is modelled as additionalProperties — a map — rather
// than as named properties "0", "1", "2", ....
func TestMergeNumericKeyedMap(t *testing.T) {
	s := merge(t, "Batch", `{"type":"batch","items":{"0":{"id":"a"},"1":{"id":"b"}}}`)

	items := s.Properties["items"].Value.Schema

	if !items.Type.Contains(asyncapi.TypeObject) {
		t.Fatalf("items.type: got %s, want object", items.Type)
	}

	if items.AdditionalProperties == nil {
		t.Fatal("items should have additionalProperties, not named properties")
	}

	if len(items.Properties) != 0 {
		t.Errorf("items should have no named properties, got %v", items.Properties)
	}
}

// TestMergeMaskedFieldHasNoExample checks that a field whose name looks like a
// credential keeps its type but drops the example — the value it happened to
// carry in this one recording has no business appearing in a public spec.
func TestMergeMaskedFieldHasNoExample(t *testing.T) {
	s := merge(t, "Auth", `{"type":"auth","session":"***"}`)

	session := s.Properties["session"].Value.Schema

	if len(session.Examples) != 0 {
		t.Errorf("session.examples: got %v, want none", session.Examples)
	}
}
