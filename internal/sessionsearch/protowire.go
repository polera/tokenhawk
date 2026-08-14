package sessionsearch

import "encoding/binary"

// Minimal protobuf wire-format reader for on-demand transcript decoding.
// Only the accessor shapes the provider readers need exist here: descending
// through length-delimited submessages and reading one string or varint. No
// schema is compiled and unknown fields are never materialized, which keeps
// undocumented payload content opaque.

// protoString returns the first UTF-8 value at the field-number path inside
// a wire-format message.
func protoString(data []byte, path ...int) (string, bool) {
	value, ok := protoBytes(data, path...)
	if !ok {
		return "", false
	}
	return string(value), true
}

// protoBytes descends length-delimited fields along path and returns the
// payload of the final field.
func protoBytes(data []byte, path ...int) ([]byte, bool) {
	current := data
	for _, number := range path {
		value, ok := firstDelimited(current, number)
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

// protoVarint reads the varint field named by the last path element after
// descending any leading length-delimited path elements.
func protoVarint(data []byte, path ...int) (uint64, bool) {
	if len(path) == 0 {
		return 0, false
	}
	current := data
	if len(path) > 1 {
		parent, ok := protoBytes(data, path[:len(path)-1]...)
		if !ok {
			return 0, false
		}
		current = parent
	}
	number := path[len(path)-1]
	for len(current) > 0 {
		field, wire, next, ok := readTag(current)
		if !ok {
			return 0, false
		}
		current = next
		if wire == 0 {
			value, n := binary.Uvarint(current)
			if n <= 0 {
				return 0, false
			}
			if field == number {
				return value, true
			}
			current = current[n:]
			continue
		}
		current, ok = skipValue(current, wire)
		if !ok {
			return 0, false
		}
	}
	return 0, false
}

func firstDelimited(data []byte, number int) ([]byte, bool) {
	for len(data) > 0 {
		field, wire, next, ok := readTag(data)
		if !ok {
			return nil, false
		}
		data = next
		if wire == 2 {
			value, rest, ok := readDelimited(data)
			if !ok {
				return nil, false
			}
			if field == number {
				return value, true
			}
			data = rest
			continue
		}
		data, ok = skipValue(data, wire)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

// readDelimited splits a length-delimited value from the front of data.
func readDelimited(data []byte) (value, rest []byte, ok bool) {
	length, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, nil, false
	}
	body := data[n:]
	if length > uint64(len(body)) {
		return nil, nil, false
	}
	return body[:length], body[length:], true
}

func readTag(data []byte) (field, wire int, rest []byte, ok bool) {
	key, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0, nil, false
	}
	return int(key >> 3), int(key & 7), data[n:], true
}

func skipValue(data []byte, wire int) ([]byte, bool) {
	switch wire {
	case 0:
		_, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, false
		}
		return data[n:], true
	case 1:
		if len(data) < 8 {
			return nil, false
		}
		return data[8:], true
	case 2:
		_, rest, ok := readDelimited(data)
		return rest, ok
	case 5:
		if len(data) < 4 {
			return nil, false
		}
		return data[4:], true
	default:
		// Group wire types are obsolete and unused by AGY payloads.
		return nil, false
	}
}
