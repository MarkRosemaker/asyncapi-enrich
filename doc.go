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
// arrived, and a declared condition — given to [Recorder], not stored in the file — that
// says when to stop listening.
//
// # Recording
//
// You author a session's URI, its send frames, and optionally a [Session.Unsubscribe]
// frame. [Recorder.Record] dials every session in a file concurrently, plays each
// one's send frames in order, and commits every frame that arrives — masked, and
// saved to disk — until [Recorder.Until] is met or its timeout expires, then performs
// the WebSocket closing handshake. A session whose existing frames already satisfy
// [Recorder.Until] is left alone and reported as skipped, so a rerun after a
// successful capture costs nothing.
//
// What a server puts inside a payload is what [Masker] exists for: every frame is
// masked before it reaches disk, as it is captured, never after.
//
// # Enriching
//
// [Enrich] turns recorded sessions into an AsyncAPI document: a server per host, one
// channel per server — every WebSocket API recorded so far multiplexes all of its
// messages over a single connection, so there is no per-topic address to key more than
// one channel by — a send and a receive operation, and a payload schema per message
// kind, inferred from the frames and merged across every one observed.
//
// One recording is not enough to describe an API on its own. A single frame cannot
// tell you which of its fields are optional, and a session only observes the message
// kinds that happened to occur while it was listening. Merging is what answers the
// question the whole tool exists to ask: a field present in every sample merged into a
// schema is required; a field present in only some of them is not, and no single
// recording — however carefully read — can tell the two apart alone.
//
// Enrich is safe to call more than once, on more than one recording: a server,
// channel, message, or schema already in the document is extended rather than
// duplicated.
//
// [AsyncAPI]: https://www.asyncapi.com
// [openapi-enrich]: https://github.com/MarkRosemaker/openapi-enrich
package enrich
