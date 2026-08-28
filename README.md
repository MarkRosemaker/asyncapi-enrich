<div align="center" id=badges>

[![Go Reference](https://pkg.go.dev/badge/github.com/MarkRosemaker/asyncapi-enrich.svg)](https://pkg.go.dev/github.com/MarkRosemaker/asyncapi-enrich)
[![Go Report Card](https://goreportcard.com/badge/github.com/MarkRosemaker/asyncapi-enrich)](https://goreportcard.com/report/github.com/MarkRosemaker/asyncapi-enrich)
[![License: Apache](https://img.shields.io/badge/License-Apache-yellow.svg)](./LICENSE)

</div>

<h3 align="center">
  Learn an async API by listening to it.
</h3>

`asyncapi-enrich` infers an [AsyncAPI](https://www.asyncapi.com) specification from
observed traffic, the way
[`openapi-enrich`](https://github.com/MarkRosemaker/openapi-enrich) does for HTTP.

> **Status: early, but complete end to end.** Recording and enrichment both work
> and are tested — including on real captures against Finnhub and Yahoo Finance.

## The workflow

The same one, with one thing changed. You write down what you want to happen and
leave the answer blank; `go generate` fills the answer in; the specification is
enriched from it.

What changes is the unit of work. HTTP gives you a request and its one response, so
an interaction is a pair. An async API gives you a connection carrying an ordered
sequence of messages in both directions that never ends on its own — so an
interaction here is a **session**: the frames you send, the frames that come back,
when each arrived, and a declared condition saying when to stop listening.

You author this — `api/sessions.json`, named so it does not collide with
`openapi-enrich`'s `api/interactions.json`:

```json
[
  {
    "uri": "wss://ws.finnhub.io?token=$FINNHUB_API_KEY",
    "frames": [
      {"send": {"type": "subscribe", "symbol": "AAPL"}}
    ]
  }
]
```

and recording fills in the rest:

```json
      {"send": {"type": "subscribe", "symbol": "AAPL"}},
      {"at": "412ms", "receive": {"type": "trade", "data": [{"s": "AAPL", "p": 190.5}]}},
      {"at": "1.08s", "receive": {"type": "ping"}}
```

Only a receive frame gets an `at`. A send frame is exactly what you authored —
its timing is an artefact of our own scheduling, not something the server did,
so there is nothing there worth recording.

A session holds only what is needed to open a connection and what crossed it.
Everything else — what the server is called, what the messages mean, why the
session exists — belongs in the AsyncAPI document, which is the artefact this
file is here to improve. The stop condition is not in the file either: it is what
you asked the recorder for, not something the server did, so it lives on the
command line.

You can also author an `unsubscribe`, sent right before the connection closes —
the natural end of a session, symmetric to the frames it opened with:

```json
{
  "uri": "wss://ws.finnhub.io?token=$FINNHUB_API_KEY",
  "frames": [{"send": {"type": "subscribe", "symbol": "AAPL"}}],
  "unsubscribe": {"type": "unsubscribe", "symbol": "AAPL"}
}
```

Recording always ends with the real WebSocket closing handshake
([RFC 6455 §7.1.1](https://www.rfc-editor.org/rfc/rfc6455#section-7.1.1)) rather
than just cutting the connection, whether or not a session has one of these.

The URI is what the specification is enriched from, the same way `openapi-enrich`
enriches from a recorded request URL: the scheme gives the server's `protocol`,
the host and port give its `host`, the path gives the channel `address`, and a
credential in the query gives a `httpApiKey` security scheme with `in: query`.

## Four things a session has to say that a request does not

- **When to stop.** A response ends an HTTP request; nothing ends a feed. So the
  condition is declared on the command line: a timeout, a number of messages, or
  — the one worth reaching for — *one of each kind*. A specification is only complete once every
  message kind has actually been seen, and a generated reader only has to
  discriminate when there is more than one kind to tell apart.
- **What did not happen.** A timeout with conditions unmet is not a failure. *"Sixty
  seconds, four trades, never a ping"* is a fact about the API, and it is reported
  rather than swallowed.
- **When each frame arrived.** A heartbeat interval is only discoverable from
  timestamps, and a client that does not send one gets dropped.
- **That one recording is not enough.** A single frame cannot tell you which of its
  fields are optional, and a session only sees the kinds that happened to occur.
  Recordings accumulate and their inferred schemas are merged, which is
  [`openapi-merge`](https://github.com/MarkRosemaker/openapi-merge)'s job.

## Recording

```bash
go get -tool github.com/MarkRosemaker/asyncapi-enrich/cmd/asyncapi-record
```

```bash
asyncapi-record -f api/sessions.json -kinds trade=3,ping=1 -timeout 60s
```

Environment variables in a URI are expanded by the tool rather than the shell, to
keep the credential out of shell history — and the URI is written back **exactly
as authored**, so the reference survives and the expansion never reaches disk. A
URI that carries a credential outright instead is masked on the way out.

Every session in the file records at once — one session waiting on a quiet feed
does not hold up another. A session whose existing frames already satisfy the
stop condition is left alone and dials nothing, so rerunning a recording that
already succeeded costs nothing:

```
$ asyncapi-record -f testdata/finnhub/api/sessions.json -kinds trade=2,ping=1 -timeout 60s
sessions[0]: already complete, skipped — 12 received (ping=1, trade=11)
```

A session that falls short of a stricter condition than last time — `-messages 5`
where only 3 were captured before, say — is not extended. What it had came from a
different connection with its own clock; it is discarded, keeping only what was
authored, and recorded again from scratch.

Every frame is written to the file as it arrives, not only once recording
finishes, so a crash mid-run loses at most the one frame in flight.

## Enriching

```bash
go get -tool github.com/MarkRosemaker/asyncapi-enrich/cmd/asyncapi-enrich
```

```bash
asyncapi-enrich -spec api/asyncapi.json -sessions api/sessions.json
```

This is the half that needs no network, split out from recording for the same
reason recording was split out from everything else: the machine that captured
the traffic is not necessarily the machine — or the moment — you write the spec
on. If `api/asyncapi.json` does not exist yet, it starts from
[`enrich.NewDocument`](https://pkg.go.dev/github.com/MarkRosemaker/asyncapi-enrich#NewDocument),
a minimal valid AsyncAPI 3.1 document, the same way `openapi-enrich` does.

For every session, it adds a server for the URI's host, one channel on that
server — every WebSocket API recorded so far multiplexes all of its messages
over a single connection, so there is no per-topic address to key more than one
channel by — a `send` and a `receive` operation, and a message with an inferred
payload schema for each distinct kind of frame observed. A frame's kind comes
from a top-level `type` field when it has one (Finnhub's `{"type": "trade", ...}`);
failing that, from its one top-level key when it has exactly one (Yahoo's
`{"subscribe": [...]}`, which has no `type` at all); failing that, every frame
going the same direction collapses into one message named after that
direction — an honest "we don't know how to tell these apart," not a guess.

A query parameter that looks like a credential — the same field names
[masking](#masking) checks against — becomes an `httpApiKey` security scheme
named after it, referenced from the server: the credential the recorder found
in Finnhub's URI is what tells enrichment the API needs one, and under what name.

Running it again — after a longer recording, or a second one against a
different symbol — extends what is already there instead of duplicating it: a
server, channel, message, or schema enrichment already produced is found and
merged into, not recreated. That merge is where the tool earns its keep. A
schema starts out requiring every field the one sample it came from happened to
carry; the moment a second sample is merged in without one of those fields,
that field stops being required. No single recording can tell "always present"
from "just happened to be there this time" apart — only merging across more
than one can, which is the reason two recordings beat one no matter how long
either of them ran.

AsyncAPI's schema is JSON Schema, whose `type` keyword is natively a list, so a
field seen as both a real value and `null` merges into an honest union —
`["number", "null"]` — rather than needing the workarounds OpenAPI 3.0 schemas
required (see `merge.go`'s doc comment for how this compares to
[`openapi-merge`](https://github.com/MarkRosemaker/openapi-merge), which this
package deliberately does not depend on).

## Masking

Every frame is masked before it reaches disk — as it is captured, not once at
the end, so an incremental save mid-recording never writes a secret either.
There is no option to turn masking off, because there is no good reason to want
one — a credential committed to a public specification repository is a
credential to rotate.

Values are replaced by field name, at any depth, case-insensitively and ignoring
separators, so `api_key`, `apiKey` and `API-KEY` are one name. A field whose value
is an object or an array is replaced whole.

`Masker.URL` additionally masks credentials in a URL's query string and user
information. That is the case `openapi-enrich`'s masker does not cover and this one
must: a feed dialled as `wss://host/?token=…` puts the secret in the URL itself,
where masking headers and bodies never reaches it.

## The asyncapi family

| Module | Purpose |
|---|---|
| [asyncapi](https://github.com/MarkRosemaker/asyncapi) | Parse, validate, and write AsyncAPI 3.x specifications |
| **asyncapi-enrich** (this module) | Infer specification content from observed traffic |
| [asyncapi-codegen](https://github.com/MarkRosemaker/asyncapi-codegen) | Generate Go types, clients, and servers from a specification |

## Additional Information

- [**Go Reference**](https://pkg.go.dev/github.com/MarkRosemaker/asyncapi-enrich): The Go reference documentation for the asyncapi-enrich package.
- [**Go Report Card**](https://goreportcard.com/report/github.com/MarkRosemaker/asyncapi-enrich): Check the code quality report.

### Dependencies

| Package | Purpose |
|---|---|
| [`github.com/MarkRosemaker/asyncapi`](https://github.com/MarkRosemaker/asyncapi) | AsyncAPI 3.x data structures |
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | The WebSocket client used to record |
| [`github.com/go-api-libs/types`](https://github.com/go-api-libs/types) | Email format validation, for string format detection |
| [`github.com/google/uuid`](https://github.com/google/uuid) | UUID format detection |

## Contributing

If you have any contributions to make, please submit a pull request or open an issue on the [GitHub repository](https://github.com/MarkRosemaker/asyncapi-enrich).

## License

This project is licensed under the [Apache 2.0 License](./LICENSE).
