package asyncapi

import "slices"

// Format defines additional formats to provide fine detail for primitive data types.
//
// The format property is an open string-valued property, and can have any value to support
// documentation needs, so an unknown format is not an error.
// ([Specification])
//
// [Specification]: https://github.com/asyncapi/spec/blob/master/spec/asyncapi.md#dataTypeFormat
type Format string

const (
	// FormatInt32 represents a signed 32 bits integer.
	FormatInt32 Format = "int32"
	// FormatInt64 represents a signed 64 bits integer.
	FormatInt64 Format = "int64"
	// FormatFloat represents a float number.
	FormatFloat Format = "float"
	// FormatDouble represents a double number.
	FormatDouble Format = "double"
	// FormatByte represents base64 encoded characters.
	FormatByte Format = "byte"
	// FormatBinary represents any sequence of octets.
	FormatBinary Format = "binary"
	// FormatDate represents a date as defined by full-date in RFC3339.
	FormatDate Format = "date"
	// FormatDateTime represents a date-time as defined by date-time in RFC3339.
	FormatDateTime Format = "date-time"
	// FormatPassword is a hint to UIs that the input needs to be obscured.
	FormatPassword Format = "password"
	// FormatUUID represents a UUID. Not one of the formats the AsyncAPI
	// Specification itself defines (see [Format.IsKnown]), but one of the
	// JSON Schema formats the specification explicitly permits.
	FormatUUID Format = "uuid"
	// FormatURI represents a URI, as defined by JSON Schema. See
	// [Format.IsKnown]'s doc comment.
	FormatURI Format = "uri"
	// FormatEmail represents an email address, as defined by JSON Schema.
	// See [Format.IsKnown]'s doc comment.
	FormatEmail Format = "email"
	// FormatIPv4 represents an IPv4 address, as defined by JSON Schema. See
	// [Format.IsKnown]'s doc comment.
	FormatIPv4 Format = "ipv4"
	// FormatIPv6 represents an IPv6 address, as defined by JSON Schema. See
	// [Format.IsKnown]'s doc comment.
	FormatIPv6 Format = "ipv6"
)

// allFormats are the formats defined by the AsyncAPI Specification.
var allFormats = []Format{
	FormatInt32, FormatInt64,
	FormatFloat, FormatDouble,
	FormatByte, FormatBinary,
	FormatDate, FormatDateTime,
	FormatPassword,
}

// IsKnown reports whether the format is one of the formats defined by the AsyncAPI Specification.
//
// Formats such as "email" or "uuid" can be used even though they are not defined by the
// specification, so a format that is not known is not necessarily invalid.
func (f Format) IsKnown() bool { return slices.Contains(allFormats, f) }
