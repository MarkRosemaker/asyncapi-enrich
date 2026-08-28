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

> **Status: early.** Recording works and is tested end to end. Inferring the
> specification from a recording is next.

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

A session holds only what is needed to open a connection and what crossed it.
Everything else — what the server is called, what the messages mean, why the
session exists — belongs in the AsyncAPI document, which is the artefact this
file is here to improve. The stop condition is not in the file either: it is what
you asked the recorder for, not something the server did, so it lives on the
command line.

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

## Masking

Every frame is masked before it reaches disk, never after. There is no option to
turn it off, because there is no good reason to want one — a credential committed
to a public specification repository is a credential to rotate.

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
| [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) | The WebSocket client used to record |

## Contributing

If you have any contributions to make, please submit a pull request or open an issue on the [GitHub repository](https://github.com/MarkRosemaker/asyncapi-enrich).

## License

This project is licensed under the [Apache 2.0 License](./LICENSE).
