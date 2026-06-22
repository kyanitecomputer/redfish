// Package odata provides the small, reflection-free runtime that generated
// Redfish resources link against: a JSON object builder, OData envelope helpers,
// reference links, collection rendering, and standard error bodies.
//
// Nothing here uses encoding/json or reflect. JSON is emitted by appending to a
// byte slice so that the linker can eliminate everything a given binary does not
// reference, and so integer and null semantics match the Redfish wire format
// exactly (integers as JSON numbers, explicit null for known-but-unavailable
// values).
package odata

import "strconv"

const hexDigits = "0123456789abcdef"

// AppendString appends s to dst as a quoted, escaped JSON string.
func AppendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// Builder incrementally constructs a JSON object with ordered keys by appending
// to an internal buffer. The zero value is not usable; call NewBuilder.
type Builder struct {
	buf   []byte
	dirty bool
}

// NewBuilder returns a Builder writing an empty JSON object.
func NewBuilder() *Builder { return &Builder{buf: make([]byte, 0, 256)} }

func (b *Builder) key(name string) {
	if b.dirty {
		b.buf = append(b.buf, ',')
	}
	b.dirty = true
	b.buf = AppendString(b.buf, name)
	b.buf = append(b.buf, ':')
}

// String writes a string member.
func (b *Builder) String(name, v string) {
	b.key(name)
	b.buf = AppendString(b.buf, v)
}

// OptString writes a string member only when v is non-empty.
func (b *Builder) OptString(name, v string) {
	if v != "" {
		b.String(name, v)
	}
}

// Int writes an integer member as a JSON number.
func (b *Builder) Int(name string, v int64) {
	b.key(name)
	b.buf = strconv.AppendInt(b.buf, v, 10)
}

// OptInt writes an integer member when v is non-nil, otherwise nothing.
func (b *Builder) OptInt(name string, v *int64) {
	if v != nil {
		b.Int(name, *v)
	}
}

// Float writes a floating-point member as a JSON number.
func (b *Builder) Float(name string, v float64) {
	b.key(name)
	b.buf = strconv.AppendFloat(b.buf, v, 'g', -1, 64)
}

// OptFloat writes a floating-point member when v is non-nil, otherwise nothing.
func (b *Builder) OptFloat(name string, v *float64) {
	if v != nil {
		b.Float(name, *v)
	}
}

// StrArray writes a JSON array of strings. A nil slice is omitted.
func (b *Builder) StrArray(name string, vs []string) {
	if vs == nil {
		return
	}
	b.key(name)
	b.buf = append(b.buf, '[')
	for i, v := range vs {
		if i > 0 {
			b.buf = append(b.buf, ',')
		}
		b.buf = AppendString(b.buf, v)
	}
	b.buf = append(b.buf, ']')
}

// Bool writes a boolean member.
func (b *Builder) Bool(name string, v bool) {
	b.key(name)
	b.buf = strconv.AppendBool(b.buf, v)
}

// OptBool writes a boolean member when v is non-nil, otherwise nothing.
func (b *Builder) OptBool(name string, v *bool) {
	if v != nil {
		b.Bool(name, *v)
	}
}

// Null writes an explicit JSON null member, used for known-but-unavailable
// values (for example an offline sensor reading).
func (b *Builder) Null(name string) {
	b.key(name)
	b.buf = append(b.buf, 'n', 'u', 'l', 'l')
}

// Raw writes a member whose value is already-encoded JSON (for example a nested
// object produced by another Builder).
func (b *Builder) Raw(name string, raw []byte) {
	b.key(name)
	b.buf = append(b.buf, raw...)
}

// Ref writes a Redfish reference link member of the form {"@odata.id": id}.
func (b *Builder) Ref(name, id string) {
	b.key(name)
	b.buf = appendRef(b.buf, id)
}

// RefArray writes a JSON array of reference links, the standard shape for
// collection members and navigation arrays.
func (b *Builder) RefArray(name string, ids []string) {
	b.key(name)
	b.buf = append(b.buf, '[')
	for i, id := range ids {
		if i > 0 {
			b.buf = append(b.buf, ',')
		}
		b.buf = appendRef(b.buf, id)
	}
	b.buf = append(b.buf, ']')
}

// RawArray writes a JSON array whose elements are already-encoded JSON, used
// for expanded (inline) members.
func (b *Builder) RawArray(name string, items [][]byte) {
	b.key(name)
	b.buf = append(b.buf, '[')
	for i, it := range items {
		if i > 0 {
			b.buf = append(b.buf, ',')
		}
		b.buf = append(b.buf, it...)
	}
	b.buf = append(b.buf, ']')
}

func appendRef(dst []byte, id string) []byte {
	dst = append(dst, '{')
	dst = AppendString(dst, "@odata.id")
	dst = append(dst, ':')
	dst = AppendString(dst, id)
	return append(dst, '}')
}

// Bytes returns the encoded JSON object. It does not mutate the Builder and may
// be called more than once.
func (b *Builder) Bytes() []byte {
	out := make([]byte, 0, len(b.buf)+2)
	out = append(out, '{')
	out = append(out, b.buf...)
	return append(out, '}')
}
