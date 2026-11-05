package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	lifecycle "example.org/lifecycle-core"
)

// Protocol constants. PROTOCOL.md states each value.
const (
	Version    = "1.0"
	major      = 1
	minor      = 0
	MaxFrame   = 1048576
	maxMessage = 1024
	maxID      = 128
	maxDepth   = 1000
)

// Handler runs one Host request. *lifecycle.Host is a Handler.
type Handler interface {
	Handle(request any) (any, error)
}

// Adapter maps protocol frames to Host requests. It sends one Host request at
// a time, also when many connections are open.
type Adapter struct {
	Host    Handler
	Schemas *Schemas
	mu      sync.Mutex
}

// NewAdapter makes an adapter for one Host.
func NewAdapter(host Handler, schemas *Schemas) *Adapter {
	return &Adapter{Host: host, Schemas: schemas}
}

func requestID(request lifecycle.Object) string {
	if s, ok := request["request_id"].(string); ok {
		if n := utf8.RuneCountInString(s); n >= 1 && n <= maxID {
			return s
		}
	}
	return ""
}

// checkVersion runs before all other checks, as PROTOCOL.md states.
func (a *Adapter) checkVersion(request lifecycle.Object) error {
	version, ok := request["protocol"]
	if !ok {
		return invalid("missing field: protocol")
	}
	env := a.Schemas.Envelope
	if err := a.Schemas.Validate(env["version"].(lifecycle.Object), version, "protocol", env); err != nil {
		return err
	}
	left, right, _ := strings.Cut(version.(string), ".")
	x, _ := strconv.Atoi(left)
	y, _ := strconv.Atoi(right)
	if x != major || y > minor {
		return &requestError{"unsupported_version", "this receiver supports protocol " + Version + " only"}
	}
	return nil
}

func (a *Adapter) check(request lifecycle.Object) (string, error) {
	if err := a.checkVersion(request); err != nil {
		return "", err
	}
	env := a.Schemas.Envelope
	if err := a.Schemas.Validate(env["request"].(lifecycle.Object), request, "request", env); err != nil {
		return "", err
	}
	op := request["operation"].(string)
	defs, known := a.Schemas.Operations[op]
	if !known {
		return "", invalid("unknown operation")
	}
	return op, a.Schemas.Validate(defs["request_body"].(lifecycle.Object), request["body"], "body", defs)
}

// Message maps one parsed request object to one response object.
func (a *Adapter) Message(request lifecycle.Object) lifecycle.Object {
	id := requestID(request)
	op, err := a.check(request)
	if err != nil {
		var re *requestError
		if errors.As(err, &re) {
			return failure(id, re.code, "protocol", re.message)
		}
		return internal(id)
	}
	q := lifecycle.Object{"op": op}
	for k, v := range request["body"].(lifecycle.Object) {
		q[k] = v
	}
	result, err := a.handle(q)
	var fault *lifecycle.Fault
	switch {
	case errors.As(err, &fault):
		return failure(id, fault.Code, fault.Stage, fault.Message)
	case err != nil:
		return internal(id)
	}
	return lifecycle.Object{"protocol": Version, "request_id": id, "status": "ok", "result": result}
}

// errPanic marks a Host panic. The response does not show the panic text.
var errPanic = errors.New("the host stopped with a panic")

func (a *Adapter) handle(q lifecycle.Object) (result any, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, errPanic
		}
	}()
	return a.Host.Handle(q)
}

// Frame gives the response object for one frame payload, and true if the
// connection must close.
func (a *Adapter) Frame(payload []byte) (lifecycle.Object, bool) {
	if len(payload) == 0 {
		return failure("", "invalid_frame", "protocol", "empty frame"), true
	}
	request, err := parse(payload)
	if err != nil {
		return failure("", "invalid_request", "protocol", err.Error()), false
	}
	return limit(a.Message(request)), false
}

// parse is strict JSON: UTF-8, one object, no duplicate keys, integers only,
// and each integer in the I-JSON safe range.
func parse(raw []byte) (lifecycle.Object, error) {
	value, err := lifecycle.Decode(raw)
	if err != nil {
		var fault *lifecycle.Fault
		if errors.As(err, &fault) {
			return nil, errors.New(fault.Message)
		}
		return nil, err
	}
	if _, err := Canonical(value); err != nil {
		if hasFloat(value) {
			return nil, errors.New("numbers must be integers")
		}
		return nil, err
	}
	object, ok := value.(lifecycle.Object)
	if !ok {
		return nil, errors.New("a request must be a JSON object")
	}
	return object, nil
}

func hasFloat(v any) bool {
	switch x := v.(type) {
	case float64:
		return true
	case lifecycle.Array:
		for _, y := range x {
			if hasFloat(y) {
				return true
			}
		}
	case lifecycle.Object:
		for _, y := range x {
			if hasFloat(y) {
				return true
			}
		}
	}
	return false
}

// Serve reads length-prefixed frames until the end of the stream or a closing
// error. A short read is not the end of the stream.
func (a *Adapter) Serve(r io.Reader, w io.Writer) error {
	for {
		header := readExact(r, 4)
		if len(header) == 0 {
			return nil
		}
		if len(header) < 4 {
			return writeFrame(w, failure("", "invalid_frame", "protocol", "truncated frame header"))
		}
		size := binary.BigEndian.Uint32(header)
		if size > MaxFrame {
			return writeFrame(w, failure("", "invalid_frame", "protocol", "frame exceeds the byte bound"))
		}
		payload := readExact(r, int(size))
		if len(payload) < int(size) {
			return writeFrame(w, failure("", "invalid_frame", "protocol", "truncated frame payload"))
		}
		response, closing := a.Frame(payload)
		if err := writeFrame(w, response); err != nil {
			return err
		}
		if closing {
			return nil
		}
	}
}

// readExact reads until it has size bytes or the stream ends. An error from
// the reader also ends the stream.
func readExact(r io.Reader, size int) []byte {
	buf := make([]byte, size)
	n, _ := io.ReadFull(r, buf)
	return buf[:n]
}

func internal(id string) lifecycle.Object {
	return failure(id, "internal_error", "host", "the host failed with an unexpected error")
}

// failure makes a diagnostic response. Each invalid UTF-8 byte in the message
// becomes a \xNN escape. A message longer than 1024 code points is truncated.
func failure(id, code, stage, message string) lifecycle.Object {
	if !utf8.ValidString(message) {
		var b strings.Builder
		for i := 0; i < len(message); {
			r, size := utf8.DecodeRuneInString(message[i:])
			if r == utf8.RuneError && size == 1 {
				fmt.Fprintf(&b, `\x%02x`, message[i])
			} else {
				b.WriteString(message[i : i+size])
			}
			i += size
		}
		message = b.String()
	}
	if utf8.RuneCountInString(message) > maxMessage {
		runes := []rune(message)
		message = string(runes[:maxMessage-3]) + "..."
	}
	return lifecycle.Object{"protocol": Version, "request_id": id, "status": code,
		"diagnostic": lifecycle.Object{"code": code, "stage": stage, "message": message}}
}

// limit gives a response that encodes and that is in the frame bound. A result
// outside the value domain gives internal_error. A result over the bound
// gives response_too_large.
func limit(response lifecycle.Object) lifecycle.Object {
	id, _ := response["request_id"].(string)
	body, err := Canonical(response)
	if err != nil {
		return internal(id)
	}
	if len(body) <= MaxFrame {
		return response
	}
	return failure(id, "response_too_large", "protocol", "the response exceeds the frame byte bound")
}

// writeFrame writes one complete frame. After a short write, it writes the
// remaining bytes again.
func writeFrame(w io.Writer, response lifecycle.Object) error {
	body, err := Canonical(limit(response))
	if err != nil {
		return err
	}
	data := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(data, uint32(len(body)))
	data = append(data, body...)
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return errors.New("the writer accepted no bytes")
		}
	}
	return nil
}
