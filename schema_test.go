package enrich_test

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// enrichOne enriches a single session with one send frame carrying payload,
// and returns the resulting document.
func enrichOne(t *testing.T, uri, payload string) *asyncapi.Document {
	t.Helper()

	doc := enrich.NewDocument()
	ss := enrich.Sessions{{
		URI:    uri,
		Frames: []*enrich.Frame{{Send: jsontext.Value(payload)}},
	}}

	if err := enrich.Enrich(doc, ss); err != nil {
		t.Fatalf("enriching: %v", err)
	}

	if err := doc.Validate(); err != nil {
		t.Fatalf("the enriched document does not validate: %v", err)
	}

	return doc
}

// TestFormatDetection checks that a string value's format is inferred, the
// same set of formats openapi-enrich detects: UUID, URI, email, RFC3339
// date-time, and IPv4/IPv6.
func TestFormatDetection(t *testing.T) {
	for name, tc := range map[string]struct{ value, wantFormat string }{
		"uuid":      {"f47ac10b-58cc-4372-a567-0e02b2c3d479", "uuid"},
		"uri":       {"https://example.com/path", "uri"},
		"email":     {"a@example.com", "email"},
		"date-time": {"2026-08-28T12:00:00Z", "date-time"},
		"ipv4":      {"192.0.2.1", "ipv4"},
		"ipv6":      {"2001:db8::1", "ipv6"},
		"plain":     {"just a string", ""},
	} {
		t.Run(name, func(t *testing.T) {
			doc := enrichOne(t, "ws://example.invalid", `{"type":"x","v":"`+tc.value+`"}`)

			v := doc.Components.Schemas["X"].Value.Schema.Properties["v"].Value.Schema
			if string(v.Format) != tc.wantFormat {
				t.Errorf("format: got %q, want %q", v.Format, tc.wantFormat)
			}
		})
	}
}

// TestNullBecomesItsOwnType checks the divergence from openapi-enrich this
// package is built on: AsyncAPI's schema type is a real JSON Schema list, so a
// literal null gets an honest null type rather than an object-shaped
// placeholder standing in for "unknown".
func TestNullBecomesItsOwnType(t *testing.T) {
	doc := enrichOne(t, "ws://example.invalid", `{"type":"x","v":null}`)

	v := doc.Components.Schemas["X"].Value.Schema.Properties["v"].Value.Schema

	if len(v.Type) != 1 || v.Type[0] != asyncapi.TypeNull {
		t.Errorf("v.type: got %s, want [null]", v.Type)
	}
}

// TestServerFromURI checks that a server's host, protocol and pathname are
// derived from the session's URI, and that a query parameter shaped like a
// credential becomes an httpApiKey security scheme naming that parameter.
func TestServerFromURI(t *testing.T) {
	doc := enrichOne(t, "wss://ws.example.com/socket?token=$EXAMPLE_TOKEN", `{"type":"x"}`)

	srv, ok := doc.Servers["ws-example-com"]
	if !ok {
		t.Fatalf("no server named ws-example-com; have %v", serverKeys(doc))
	}

	if srv.Value.Host != "ws.example.com" {
		t.Errorf("host: got %q, want ws.example.com", srv.Value.Host)
	}

	if srv.Value.Protocol != asyncapi.ProtocolWebSockets {
		t.Errorf("protocol: got %q, want ws", srv.Value.Protocol)
	}

	if srv.Value.Pathname != "/socket" {
		t.Errorf("pathname: got %q, want /socket", srv.Value.Pathname)
	}

	if len(srv.Value.Security) != 1 {
		t.Fatalf("security: got %d entries, want 1", len(srv.Value.Security))
	}

	scheme := srv.Value.Security[0].Value

	if scheme.Type != asyncapi.SecuritySchemeTypeHTTPAPIKey || scheme.In != asyncapi.SecuritySchemeInQuery || scheme.Name != "token" {
		t.Errorf("scheme: got %+v, want httpApiKey/query/token", scheme)
	}
}

func serverKeys(doc *asyncapi.Document) []string {
	keys := make([]string, 0, len(doc.Servers))
	for k := range doc.Servers {
		keys = append(keys, k)
	}

	return keys
}

// TestMessageNamedAfterSoleKey checks the Yahoo case: a payload with no "type"
// field but exactly one top-level key is named after that key, rather than
// every such message colliding under one generic name.
func TestMessageNamedAfterSoleKey(t *testing.T) {
	doc := enrich.NewDocument()
	ss := enrich.Sessions{{
		URI: "ws://example.invalid",
		Frames: []*enrich.Frame{
			{Send: jsontext.Value(`{"subscribe":["AAPL"]}`)},
			{Send: jsontext.Value(`{"unsubscribe":["AAPL"]}`)},
		},
	}}

	if err := enrich.Enrich(doc, ss); err != nil {
		t.Fatalf("enriching: %v", err)
	}

	for _, name := range []string{"Subscribe", "Unsubscribe"} {
		if _, ok := doc.Components.Schemas[name]; !ok {
			t.Errorf("missing schema %q; have %v", name, schemaNames(doc))
		}
	}
}

// TestMessageFallsBackToDirection checks that a payload with neither a "type"
// field nor exactly one top-level key falls back to being named after the
// direction it travelled — an honest "we don't know" rather than a guess.
func TestMessageFallsBackToDirection(t *testing.T) {
	doc := enrichOne(t, "ws://example.invalid", `{"a":1,"b":2}`)

	if _, ok := doc.Components.Schemas["Send"]; !ok {
		t.Errorf(`missing schema "Send"; have %v`, schemaNames(doc))
	}
}
