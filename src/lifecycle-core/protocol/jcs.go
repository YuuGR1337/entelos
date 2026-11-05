// Package protocol serves the versioned wire protocol of src/protocol/PROTOCOL.md
// in front of a lifecycle Host. The sidecar command puts it on a Unix domain
// socket.
package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	lifecycle "example.org/lifecycle-core"
)

// safeInt is 2^53 - 1, the I-JSON safe integer bound.
const safeInt = 1<<53 - 1

var errDomain = errors.New("value outside the protocol value domain")

// Canonical gives the RFC 8785 canonical form of a value in the protocol
// value domain: null, bool, string, integer, array and object. The domain has
// no floats. An integer must be in the I-JSON safe range. A string must be
// valid UTF-8.
func Canonical(value any) ([]byte, error) {
	var b bytes.Buffer
	if err := canonical(&b, value, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func canonical(b *bytes.Buffer, value any, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: nesting is too deep", errDomain)
	}
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case lifecycle.Int:
		n, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return fmt.Errorf("%w: invalid integer text", errDomain)
		}
		if n.CmpAbs(big.NewInt(safeInt)) > 0 {
			return fmt.Errorf("%w: integer outside the I-JSON safe range", errDomain)
		}
		b.WriteString(n.String())
	case int:
		return canonical(b, int64(v), depth)
	case int64:
		if v > safeInt || v < -safeInt {
			return fmt.Errorf("%w: integer outside the I-JSON safe range", errDomain)
		}
		b.WriteString(strconv.FormatInt(v, 10))
	case string:
		return canonicalString(b, v)
	case []string:
		items := make(lifecycle.Array, len(v))
		for i, s := range v {
			items[i] = s
		}
		return canonical(b, items, depth)
	case lifecycle.Array:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonical(b, item, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case lifecycle.Object:
		keys := make([]string, 0, len(v))
		for k := range v {
			if !utf8.ValidString(k) {
				return fmt.Errorf("%w: an object key is not valid UTF-8", errDomain)
			}
			keys = append(keys, k)
		}
		// RFC 8785 sorts the keys by their UTF-16 code units.
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonicalString(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := canonical(b, v[k], depth+1); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("%w: %T", errDomain, value)
	}
	return nil
}

func utf16Less(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// canonicalString writes a JSON string as RFC 8785 section 3.2.2.2 states.
func canonicalString(b *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: a string is not valid UTF-8", errDomain)
	}
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return nil
}
