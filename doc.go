// Package enrich infers an [AsyncAPI] specification from observed traffic, the way
// [openapi-enrich] does for HTTP.
//
// The workflow is the same one: inside the library you want to generate, you write
// down what you want to happen and leave the answer blank, run go generate, and the
// tool fills the answer in and enriches the specification from it.
//
// What differs is the unit of work. HTTP gives you a request and its one response, so
// an interaction is a pair. An async API gives you a connection that carries an ordered
// sequence of messages in both directions and never ends on its own, so an interaction
// here is a [Session]: the frames you send, the frames that come back, when each of them
// arrived, and a declared condition that says when to stop listening.
//
// # Recording
//
// You author a session's URI, its send frames, and optionally an [Session.Unsubscribe]
// frame. [Recorder.Record] dials every session in a file concurrently, plays each
// one's send frames in order, and commits every frame that arrives — masked, and
// saved to disk — until [Recorder.Until] is met or its timeout expires, then performs
// the WebSocket closing handshake. A session whose existing frames already satisfy
// [Recorder.Until] is left alone and reported as skipped, so a rerun after a
// successful capture costs nothing.
//
// A session names its server by key rather than by URL, so the credentials used to dial
// never enter the recording. What a server puts inside a payload is a different matter,
// and is what [Masker] is for.
//
// # Merging
//
// One recording is not enough to describe an API. A single frame cannot tell you which
// of its fields are optional, and a session only observes the message kinds that
// happened to occur while it was listening. Recordings therefore accumulate, and the
// schemas inferred from them are merged.
//
// [AsyncAPI]: https://www.asyncapi.com
// [openapi-enrich]: https://github.com/MarkRosemaker/openapi-enrich
package enrich
