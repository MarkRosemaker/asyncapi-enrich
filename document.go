package enrich

import "github.com/MarkRosemaker/asyncapi"

// NewDocument creates a minimal valid AsyncAPI 3.1.0 document as a starting point.
func NewDocument() *asyncapi.Document {
	return &asyncapi.Document{
		AsyncAPI:           "3.1.0",
		Info:               &asyncapi.Info{Title: "API", Version: "0.0.1"},
		DefaultContentType: asyncapi.MediaTypeJSON,
	}
}
