package sessionmove

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Schema 2 freezes the entire actionable object as one ASCII JSON line, with
// no trailing Enter. Herdr adds the sole authorized submission key. Escaping
// occurs before durable admission and hashing; recorded bytes never change.
func encodeASCIIMessage(message Message) ([]byte, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	out.Grow(len(raw))
	hex := "0123456789abcdef"
	appendEscape := func(value uint16) {
		out.WriteString(`\u`)
		out.WriteByte(hex[value>>12])
		out.WriteByte(hex[(value>>8)&15])
		out.WriteByte(hex[(value>>4)&15])
		out.WriteByte(hex[value&15])
	}
	for _, r := range string(raw) {
		if r < 128 {
			out.WriteByte(byte(r))
			continue
		}
		if r <= 0xffff {
			appendEscape(uint16(r))
		} else {
			high, low := utf16.EncodeRune(r)
			appendEscape(uint16(high))
			appendEscape(uint16(low))
		}
	}
	return []byte(out.String()), nil
}

// RequireActionableMessage separates immutable legacy readers from active
// delivery. No retry silently rewrites an admitted schema-1 object or digest.
func RequireActionableMessage(message Message) error {
	if message.SchemaVersion != MessageSchemaVersion {
		return fmt.Errorf("legacy session message schema %d is read-only; create a new message with a new message ID for Herdr delivery", message.SchemaVersion)
	}
	return message.validate()
}
