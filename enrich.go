package enrich

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/MarkRosemaker/asyncapi"
)

// discriminatorField is the payload field this package looks at to tell one
// message kind from another. It is not configurable: every fixture recorded so
// far uses "type", it is [Recorder]'s own default, and a tool inferring a
// specification from evidence should not invent a second convention no
// recording actually demonstrated a need for.
const discriminatorField = "type"

// Enrich updates doc in place from recorded sessions: it adds servers, one
// channel per server, a send and a receive operation, and payload schemas
// inferred and merged from every frame observed. It is safe to call more than
// once, on more than one recording: a server, channel, message, or schema
// already in doc is extended rather than duplicated.
//
// If a field already carries a ContentSchema declaring a protobuf message —
// something only a maintainer sets, by hand, from a real .proto; this
// package only ever infers ContentEncoding on its own (see
// [detectBinaryEncodings]) — Enrich also fails when the newly recorded
// examples do not decode as that message: see [validateProtoSchemas]. A
// maintainer who pastes in the real .proto gets told immediately when a
// recording stops matching it, rather than finding out from a schema that
// silently drifted.
func Enrich(doc *asyncapi.Document, ss Sessions) error {
	if err := ss.Validate(); err != nil {
		return err
	}

	for i, s := range ss {
		if err := enrichSession(doc, s); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}

	// Channels and operations are plain Go maps: unlike servers, schemas, and
	// security schemes, they have no exported way to append while preserving
	// insertion order, so every one this package adds starts out unordered.
	// Sorting is what makes writing the same sessions twice produce the same
	// file both times, rather than an order that happens to depend on map
	// iteration.
	doc.SortMaps()

	for _, ch := range doc.Channels {
		ch.Value.Messages.Sort()
	}

	detectBinaryEncodings(doc)

	if err := validateProtoSchemas(doc); err != nil {
		return err
	}

	return resolve(doc)
}

// resolve re-derives doc by marshalling and reloading it, so that every $ref
// this package built ends up with its Value populated exactly as it would be
// for a document read from a file — [asyncapi.Document.Validate] requires a
// reference's Value to be resolved, and only the loader does that; a $ref built
// by hand with nothing but a Reference, the way every one here is, is not.
func resolve(doc *asyncapi.Document) error {
	data, err := doc.ToJSON()
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}

	reloaded, err := asyncapi.LoadFromDataJSON(data)
	if err != nil {
		return fmt.Errorf("reloading: %w", err)
	}

	*doc = *reloaded

	return nil
}

func enrichSession(doc *asyncapi.Document, s *Session) error {
	u, err := url.Parse(s.URI)
	if err != nil {
		return fmt.Errorf("uri: %w", err)
	}

	serverKey, err := ensureServer(doc, u)
	if err != nil {
		return err
	}

	chKey := ensureChannel(doc, serverKey, u)
	ch := doc.Channels[chKey].Value

	sent, received := map[string]bool{}, map[string]bool{}

	for _, f := range s.Frames {
		payload, direction := f.Send, "send"
		to := sent

		if len(f.Receive) > 0 {
			payload, direction, to = f.Receive, "receive", received
		}

		name, err := ensureMessage(doc, ch, direction, payload)
		if err != nil {
			return fmt.Errorf("%s frame: %w", direction, err)
		}

		to[name] = true
	}

	if len(s.Unsubscribe) > 0 {
		name, err := ensureMessage(doc, ch, "send", s.Unsubscribe)
		if err != nil {
			return fmt.Errorf("unsubscribe frame: %w", err)
		}

		sent[name] = true
	}

	ensureOperation(doc, "send", serverKey, chKey, sent)
	ensureOperation(doc, "receive", serverKey, chKey, received)

	return nil
}

// ensureServer finds the server this URI already describes, or adds one, and
// returns its key. A security scheme is also added the first time a query
// parameter that looks like a credential is seen — the URI is what taught us
// the API needs one, and where.
func ensureServer(doc *asyncapi.Document, u *url.URL) (string, error) {
	for key, ref := range doc.Servers {
		if ref.Ref == nil && ref.Value.Host == u.Host {
			return key, nil
		}
	}

	key := uniqueKey(doc.Servers, hostKey(u.Host))

	srv := &asyncapi.Server{
		Host: u.Host,
		// AsyncAPI's protocol enum has one WebSocket value; it does not
		// distinguish ws from wss the way a URL scheme does; see the doc
		// comment on [asyncapi.ProtocolWebSockets].
		Protocol: asyncapi.ProtocolWebSockets,
	}

	if u.Path != "" && u.Path != "/" {
		srv.Pathname = u.Path
	}

	if err := addCredentialSecurity(doc, srv, u); err != nil {
		return "", err
	}

	doc.Servers.Set(key, &asyncapi.ServerRef{Value: srv})

	return key, nil
}

// addCredentialSecurity adds an httpApiKey security scheme for the first query
// parameter whose name looks like a credential, and attaches it to srv. This is
// the payoff of masking by field name rather than by pattern: the same field
// name that told the recorder to mask a value tells enrichment there is a
// security scheme here, and what its name is.
func addCredentialSecurity(doc *asyncapi.Document, srv *asyncapi.Server, u *url.URL) error {
	mask := NewMasker()

	for name := range u.Query() {
		if !mask.masks(name) {
			continue
		}

		if doc.Components.SecuritySchemes == nil {
			doc.Components.SecuritySchemes = asyncapi.SecuritySchemes{}
		}

		if _, ok := doc.Components.SecuritySchemes[name]; !ok {
			doc.Components.SecuritySchemes.Set(name, &asyncapi.SecuritySchemeRef{Value: &asyncapi.SecurityScheme{
				Type: asyncapi.SecuritySchemeTypeHTTPAPIKey,
				In:   asyncapi.SecuritySchemeInQuery,
				Name: name,
			}})
		}

		srv.Security = asyncapi.SecuritySchemeRefList{
			{Ref: &asyncapi.Reference{Identifier: "#/components/securitySchemes/" + name}},
		}

		return nil
	}

	return nil
}

// ensureChannel finds or creates the one channel this package puts on a
// server. Every WebSocket API recorded so far multiplexes all of its messages
// over a single connection — there is no per-topic address to key more than
// one channel by — so "one channel per server" is what the evidence supports;
// see the [asyncapi-enrich README] for the reasoning.
//
// [asyncapi-enrich README]: https://github.com/MarkRosemaker/asyncapi-enrich#readme
func ensureChannel(doc *asyncapi.Document, serverKey string, u *url.URL) string {
	for key, ref := range doc.Channels {
		if ref.Ref != nil {
			continue
		}

		for _, s := range ref.Value.Servers {
			if s.Ref != nil && s.Ref.Identifier == "#/servers/"+serverKey {
				return key
			}
		}
	}

	if doc.Channels == nil {
		doc.Channels = asyncapi.Channels{}
	}

	key := uniqueKey(doc.Channels, serverKey)

	ch := &asyncapi.Channel{
		Messages: asyncapi.Messages{},
		Servers: asyncapi.ServerRefList{
			{Ref: &asyncapi.Reference{Identifier: "#/servers/" + serverKey}},
		},
	}

	// The path, when there is one worth naming, already lives on the server as
	// its pathname (ensureServer); the channel's own address is left unset —
	// "null or absent... MUST be interpreted as unknown" — because what these
	// APIs actually address is a subscription (a symbol), which is runtime
	// data no schema should hardcode, not a fixed topic this channel could
	// name.
	doc.Channels[key] = &asyncapi.ChannelRef{Value: ch}

	return key
}

// ensureMessage infers a schema from payload, merges it into the schema of the
// message this discriminates as (creating both the message and its schema the
// first time that kind is seen), and returns the message's key.
//
// A message is named after [discriminatorField] when the payload has one.
// Failing that, a payload shaped as one key wrapping the real content — Yahoo's
// {"subscribe": [...]} and {"unsubscribe": [...]} have no "type" field, but are
// still two distinct, nameable shapes this way — is named after that key.
// Only when neither applies does naming fall back to direction, collapsing
// every differently-shaped message going the same way into one: the honest
// result of not having a name for something, not a limitation worth working
// around by inventing one.
func ensureMessage(doc *asyncapi.Document, ch *asyncapi.Channel, direction string, payload jsontext.Value) (string, error) {
	name := direction

	if kind, ok := discriminate(payload, discriminatorField); ok && kind != "" {
		name = kind
	} else if key, ok := soleKey(payload); ok {
		name = key
	}

	s, err := newSchemaFromJSON(payload)
	if err != nil {
		return "", fmt.Errorf("message %q: %w", name, err)
	}

	schemaName := pascalCase(name)

	if doc.Components.Schemas == nil {
		doc.Components.Schemas = asyncapi.Schemas{}
	}

	if existing, ok := doc.Components.Schemas[schemaName]; ok {
		if err := mergeSchema(existing.Value.Schema, s); err != nil {
			return "", fmt.Errorf("message %q: %w", name, err)
		}
	} else {
		doc.Components.Schemas.Set(schemaName, &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: s}})
	}

	if _, ok := ch.Messages[name]; !ok {
		ch.Messages[name] = &asyncapi.MessageRef{Value: &asyncapi.Message{
			Name: name,
			Payload: &asyncapi.AnySchemaRef{
				Ref: &asyncapi.Reference{Identifier: "#/components/schemas/" + schemaName},
			},
		}}
	}

	return name, nil
}

// ensureOperation adds or extends the operation for one direction of one
// channel, listing every message kind seen going that way.
func ensureOperation(doc *asyncapi.Document, direction, serverKey, chKey string, kinds map[string]bool) {
	if len(kinds) == 0 {
		return
	}

	if doc.Operations == nil {
		doc.Operations = asyncapi.Operations{}
	}

	chRef := "#/channels/" + chKey

	key := direction
	if existing, ok := doc.Operations[key]; ok &&
		(existing.Value.Channel == nil || existing.Value.Channel.Ref == nil || existing.Value.Channel.Ref.Identifier != chRef) {
		// The plain name is already another channel's operation — fall back to
		// a name that is unique to this one rather than overwrite it.
		key = uniqueKey(doc.Operations, direction+pascalCase(serverKey))
	}

	op, ok := doc.Operations[key]
	if !ok {
		op = &asyncapi.OperationRef{Value: &asyncapi.Operation{
			Action:  asyncapi.OperationAction(direction),
			Channel: &asyncapi.ChannelRef{Ref: &asyncapi.Reference{Identifier: chRef}},
		}}
		doc.Operations[key] = op
	}

	have := map[string]bool{}

	for _, m := range op.Value.Messages {
		if m.Ref != nil {
			have[strings.TrimPrefix(m.Ref.Identifier, chRef+"/messages/")] = true
		}
	}

	for _, name := range sortedKeys(kinds) {
		if have[name] {
			continue
		}

		op.Value.Messages = append(op.Value.Messages, &asyncapi.MessageRef{
			Ref: &asyncapi.Reference{Identifier: chRef + "/messages/" + name},
		})
	}
}

// soleKey returns the one top-level member name of payload, if it is a JSON
// object with exactly one.
func soleKey(payload jsontext.Value) (string, bool) {
	var obj map[string]jsontext.Value
	if err := json.Unmarshal(payload, &obj); err != nil || len(obj) != 1 {
		return "", false
	}

	for k := range obj {
		return k, true
	}

	return "", false
}

// hostKey turns a host into a valid server key: a server key may only contain
// letters, digits, underscores and hyphens, and a host has dots a key cannot.
func hostKey(host string) string {
	var b strings.Builder

	for _, r := range host {
		switch {
		case r == '.' || r == ':':
			b.WriteByte('-')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		}
	}

	return b.String()
}

// uniqueKey returns key, or key with a numeric suffix, whichever is not
// already used in m.
func uniqueKey[V any](m map[string]V, key string) string {
	if _, ok := m[key]; !ok {
		return key
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", key, i)
		if _, ok := m[candidate]; !ok {
			return candidate
		}
	}
}

// pascalCase capitalises the first letter of each word, treating '_', '-' and
// space as word separators, and joins them with none — the naming convention
// this family uses for a generated schema name.
func pascalCase(s string) string {
	var b strings.Builder

	upperNext := true

	for _, r := range s {
		switch r {
		case '_', '-', ' ':
			upperNext = true

			continue
		}

		if upperNext {
			b.WriteRune(unicode.ToUpper(r))

			upperNext = false
		} else {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// sortedKeys returns the keys of a set, sorted, so that output built from it
// does not depend on map iteration order.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
