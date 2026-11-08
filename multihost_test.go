package enrich_test

import (
	"encoding/json/jsontext"
	"slices"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

func operationKeys(doc *asyncapi.Document) []string {
	keys := make([]string, 0, len(doc.Operations))
	for k := range doc.Operations {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// TestEnrichTwoHosts checks that sessions against two hosts get a server, a
// channel and operations each, and that rerunning finds them rather than
// adding a second copy of the second host's operations.
func TestEnrichTwoHosts(t *testing.T) {
	ss := enrich.Sessions{
		{URI: "ws://a.example", Frames: []*enrich.Frame{{Send: jsontext.Value(`{"type":"sub"}`)}, {Receive: jsontext.Value(`{"type":"tick"}`)}}},
		{URI: "ws://b.example", Frames: []*enrich.Frame{{Send: jsontext.Value(`{"type":"sub"}`)}, {Receive: jsontext.Value(`{"type":"tick"}`)}}},
	}

	doc := enrich.NewDocument()

	for range 2 {
		if err := enrich.Enrich(doc, ss); err != nil {
			t.Fatalf("enriching: %v", err)
		}

		if err := doc.Validate(); err != nil {
			t.Fatalf("validating: %v", err)
		}
	}

	if len(doc.Servers) != 2 || len(doc.Channels) != 2 {
		t.Fatalf("got %d servers and %d channels, want 2 each", len(doc.Servers), len(doc.Channels))
	}

	want := []string{"receive", "receiveBExample", "send", "sendBExample"}
	if got := operationKeys(doc); !slices.Equal(got, want) {
		t.Fatalf("operations: got %v, want %v", got, want)
	}

	op := doc.Operations["sendBExample"].Value
	if op.Channel.Ref.Identifier != "#/channels/b-example" {
		t.Errorf("sendBExample channel: got %q", op.Channel.Ref.Identifier)
	}

	if len(op.Messages) != 1 {
		t.Errorf("sendBExample messages: got %d, want 1", len(op.Messages))
	}
}

// TestEnrichHostKeyCollision checks that two hosts reducing to the same
// server key still get a server each.
func TestEnrichHostKeyCollision(t *testing.T) {
	ss := enrich.Sessions{
		{URI: "ws://a.example", Frames: []*enrich.Frame{{Send: jsontext.Value(`{"type":"sub"}`)}}},
		{URI: "ws://a-example", Frames: []*enrich.Frame{{Send: jsontext.Value(`{"type":"sub"}`)}}},
	}

	doc := enrich.NewDocument()
	if err := enrich.Enrich(doc, ss); err != nil {
		t.Fatalf("enriching: %v", err)
	}

	if got := doc.Servers["a-example2"]; got == nil || got.Value.Host != "a-example" {
		t.Fatalf("servers: got %v, want a-example2 for host a-example", serverKeys(doc))
	}

	if _, ok := doc.Channels["a-example2"]; !ok {
		t.Errorf("no channel for the second server")
	}
}
