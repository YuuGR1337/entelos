package lifecycle

// The record codec gives the same bytes as the Python reference codec
// (json.dumps with sort_keys, ensure_ascii=False, compact separators and a
// final newline). Content references depend on these bytes, so both cores
// must agree byte for byte.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Object, Array and Int are the decoded JSON value types. A JSON value is
// nil, bool, string, Int, float64, Array or Object.
type (
	Object = map[string]any
	Array  = []any
)

// Int keeps a JSON integer as normalized decimal text, without a size limit.
type Int struct{ Text string }

// maxDepth is the nesting limit of the decoder. The Python decoder stops at
// its interpreter recursion limit, which is near this value.
const maxDepth = 1000

// maxIntDigits is the Python limit for integer text conversion.
const maxIntDigits = 4300

func codecFault(message string) *Fault { return NewFault("invalid_record", message, "codec") }

// Decode parses one strict JSON value. It refuses duplicate keys, non-finite
// numbers, lone surrogates, invalid UTF-8 and deep nesting.
func Decode(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, codecFault("invalid UTF-8")
	}
	p := &parser{data: raw}
	p.space()
	value, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, codecFault("extra data")
	}
	return value, nil
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) fail(message string) error {
	return codecFault(fmt.Sprintf("%s at offset %d", message, p.pos))
}

func (p *parser) value(depth int) (any, error) {
	if depth >= maxDepth {
		return nil, codecFault("JSON nesting is too deep")
	}
	if p.pos >= len(p.data) {
		return nil, p.fail("expecting value")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail("expecting value")
}

func (p *parser) literal(word string, value any) (any, error) {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		return nil, p.fail("expecting value")
	}
	p.pos += len(word)
	return value, nil
}

func (p *parser) object(depth int) (any, error) {
	p.pos++
	result := Object{}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return result, nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.fail("expecting property name")
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		name := key.(string)
		if _, seen := result[name]; seen {
			return nil, codecFault("duplicate JSON key: " + name)
		}
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, p.fail("expecting ':' delimiter")
		}
		p.pos++
		p.space()
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result[name] = value
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("expecting ',' delimiter")
		}
		if p.data[p.pos] == '}' {
			p.pos++
			return result, nil
		}
		if p.data[p.pos] != ',' {
			return nil, p.fail("expecting ',' delimiter")
		}
		p.pos++
	}
}

func (p *parser) array(depth int) (any, error) {
	p.pos++
	result := Array{}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return result, nil
	}
	for {
		p.space()
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("expecting ',' delimiter")
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return result, nil
		}
		if p.data[p.pos] != ',' {
			return nil, p.fail("expecting ',' delimiter")
		}
		p.pos++
	}
}

func (p *parser) hex4() (rune, bool) {
	if p.pos+4 > len(p.data) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(v), true
}

func (p *parser) str() (any, error) {
	p.pos++
	var b strings.Builder
	for {
		if p.pos >= len(p.data) {
			return nil, p.fail("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c < 0x20:
			return nil, p.fail("invalid control character")
		case c != '\\':
			b.WriteByte(c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.data) {
			return nil, p.fail("unterminated string")
		}
		e := p.data[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return nil, p.fail("invalid \\uXXXX escape")
			}
			if utf16.IsSurrogate(r) {
				// Only a high surrogate followed by an escaped low surrogate is a character.
				if r < 0xdc00 && p.pos+1 < len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
					save := p.pos
					p.pos += 2
					if low, ok := p.hex4(); ok && low >= 0xdc00 && low <= 0xdfff {
						b.WriteRune(utf16.DecodeRune(r, low))
						continue
					}
					p.pos = save
				}
				return nil, codecFault("lone surrogate in string")
			}
			b.WriteRune(r)
		default:
			return nil, p.fail("invalid escape")
		}
	}
}

func (p *parser) number() (any, error) {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	digits := func() int {
		n := 0
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	if p.pos < len(p.data) && p.data[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		return nil, p.fail("expecting value")
	}
	isFloat := false
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		save := p.pos
		p.pos++
		if digits() == 0 {
			p.pos = save
		} else {
			isFloat = true
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		save := p.pos
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			p.pos = save
		} else {
			isFloat = true
		}
	}
	text := string(p.data[start:p.pos])
	if !isFloat {
		body := strings.TrimPrefix(text, "-")
		if len(body) > maxIntDigits {
			return nil, codecFault("integer text exceeds the conversion limit")
		}
		if body == "0" {
			return Int{"0"}, nil
		}
		return Int{text}, nil
	}
	return parseFloat(text)
}

// parseFloat converts float text like the Python float(). An underflow gives
// zero or a subnormal value, as in Python. An overflow and any other error
// give a codec fault.
func parseFloat(text string) (any, error) {
	f, err := strconv.ParseFloat(text, 64)
	switch {
	case err == nil:
		return f, nil
	case errors.Is(err, strconv.ErrRange) && !math.IsInf(f, 0):
		return f, nil
	case errors.Is(err, strconv.ErrRange):
		return nil, codecFault("Out of range float values are not JSON compliant")
	}
	return nil, codecFault("invalid float text")
}

// Encode gives the canonical record bytes with a final newline.
func Encode(value any) []byte {
	var b bytes.Buffer
	encodeValue(&b, value)
	b.WriteByte('\n')
	return b.Bytes()
}

func encodeValue(b *bytes.Buffer, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Int:
		b.WriteString(v.Text)
	case int:
		b.WriteString(strconv.Itoa(v))
	case float64:
		b.WriteString(pythonFloat(v))
	case string:
		encodeString(b, v)
	case Array:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeValue(b, item)
		}
		b.WriteByte(']')
	case []string:
		items := make(Array, len(v))
		for i, s := range v {
			items[i] = s
		}
		encodeValue(b, items)
	case Object:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeString(b, k)
			b.WriteByte(':')
			encodeValue(b, v[k])
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("lifecycle: cannot encode %T", value))
	}
}

func encodeString(b *bytes.Buffer, s string) {
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
}

// pythonFloat gives the text of Python repr(float).
func pythonFloat(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		panic("lifecycle: non-finite float")
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mantissa, exponent, _ := strings.Cut(s, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	exp, _ := strconv.Atoi(exponent)
	point := exp + 1
	if point <= -4 || point > 16 {
		text := digits[:1]
		if len(digits) > 1 {
			text += "." + digits[1:]
		}
		esign := "+"
		if exp < 0 {
			esign, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, text, esign, exp)
	}
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}

// Media gives the media type of a lifecycle record kind.
func Media(kind string) string { return "application/vnd.cel-lifecycle." + kind + "+json" }

// Envelope binds a media type to raw bytes.
func Envelope(mime string, raw []byte) []byte {
	out := make([]byte, 0, len(mime)+1+len(raw))
	out = append(out, mime...)
	out = append(out, 0)
	return append(out, raw...)
}

// ContentRef is the content identity of an envelope. It is not a signature.
func ContentRef(envelope []byte) string {
	sum := sha256.Sum256(envelope)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// equal gives Python equality for decoded JSON values: bool compares as an
// integer, and an integer compares exactly with a float.
func equal(a, b any) bool {
	// Encode accepts []string as an array, so equal does too.
	a, b = asArray(a), asArray(b)
	an, aNum := number(a)
	bn, bNum := number(b)
	// A non-finite float has no rational value. Compare it as a float, as
	// Python does: NaN equals nothing and an infinity equals only itself.
	if x, ok := a.(float64); ok && (math.IsInf(x, 0) || math.IsNaN(x)) {
		y, ok := b.(float64)
		return ok && x == y
	}
	if y, ok := b.(float64); ok && (math.IsInf(y, 0) || math.IsNaN(y)) {
		return false
	}
	if aNum && bNum {
		return an.Cmp(bn) == 0
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case Array:
		y, ok := b.(Array)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case Object:
		y, ok := b.(Object)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !equal(v, w) {
				return false
			}
		}
		return true
	}
	return false
}

func asArray(v any) any {
	if list, ok := v.([]string); ok {
		out := make(Array, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out
	}
	return v
}

func number(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewRat(1, 1), true
		}
		return big.NewRat(0, 1), true
	case Int:
		r, ok := new(big.Rat).SetString(x.Text)
		return r, ok
	case int:
		return big.NewRat(int64(x), 1), true
	case float64:
		return new(big.Rat).SetFloat64(x), true
	}
	return nil, false
}
