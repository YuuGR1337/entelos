package protocol

// A Go runner for the vectors in src/protocol/vectors. It follows
// src/protocol/test_protocol.py: a fresh Git store for each session file, the
// steps in order, and the $capture, $use and $type markers. The expected
// values come from the vector files, not from this adapter.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lifecycle "example.org/lifecycle-core"
)

// protocolDir is absolute, because the socket tests change the working directory.
var protocolDir, _ = filepath.Abs(filepath.Join("..", "..", "protocol"))

func loadTestSchemas(t *testing.T) *Schemas {
	t.Helper()
	s, err := LoadSchemas(filepath.Join(protocolDir, "schema"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readVector(t *testing.T, name string) lifecycle.Object {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(protocolDir, "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	value, err := lifecycle.Decode(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value.(lifecycle.Object)
}

// noRuntime is the runtime of the vectors: they run no rule.
type noRuntime struct{}

func (noRuntime) Build() (string, error) {
	return "", lifecycle.NewFault("implementation_unavailable", "no runtime in protocol vectors", "runtime")
}

func (noRuntime) Execute(map[string][]byte, string, []byte, any) (any, error) {
	return nil, lifecycle.NewFault("implementation_unavailable", "no runtime in protocol vectors", "runtime")
}

// chunkReader returns at most size bytes for each read, as a raw socket can.
type chunkReader struct {
	data []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), r.size)], r.data)
	r.data = r.data[n:]
	return n, nil
}

// shortWriter accepts at most size bytes for each write and reports no
// error, as a raw socket write can.
type shortWriter struct {
	bytes.Buffer
	size  int
	calls int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.calls++
	return w.Buffer.Write(p[:min(len(p), w.size)])
}

// normal gives the decoded form of a value, so that Go values from the Host
// compare with decoded vector values.
func normal(t *testing.T, value any) any {
	t.Helper()
	raw, err := Canonical(value)
	if err != nil {
		t.Fatalf("cannot encode %v: %v", value, err)
	}
	out, err := lifecycle.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func match(expected, actual any, bound map[string]string, where string) []string {
	if e, ok := expected.(lifecycle.Object); ok && len(e) == 1 {
		if name, ok := e["$capture"].(string); ok {
			s, isString := actual.(string)
			if !isString || s == "" {
				return []string{where + ": capture needs a nonempty string"}
			}
			if old, has := bound[name]; has && old != s {
				return []string{where + ": differs from captured " + name}
			}
			bound[name] = s
			return nil
		}
		if kind, ok := e["$type"].(string); ok {
			if _, isString := actual.(string); kind == "string" && isString {
				return nil
			}
			return []string{where + ": expected a string"}
		}
	}
	switch e := expected.(type) {
	case lifecycle.Object:
		a, ok := actual.(lifecycle.Object)
		if !ok || len(a) != len(e) {
			return []string{fmt.Sprintf("%s: keys differ: %s != %s", where, lifecycle.Encode(expected), lifecycle.Encode(actual))}
		}
		var out []string
		for k, v := range e {
			w, has := a[k]
			if !has {
				return []string{where + ": missing key " + k}
			}
			out = append(out, match(v, w, bound, where+"."+k)...)
		}
		return out
	case lifecycle.Array:
		a, ok := actual.(lifecycle.Array)
		if !ok || len(a) != len(e) {
			return []string{where + ": list differs"}
		}
		var out []string
		for i := range e {
			out = append(out, match(e[i], a[i], bound, fmt.Sprintf("%s[%d]", where, i))...)
		}
		return out
	}
	if !sameValue(expected, actual) || fmt.Sprintf("%T", expected) != fmt.Sprintf("%T", actual) {
		return []string{fmt.Sprintf("%s: %s != %s", where, lifecycle.Encode(expected), lifecycle.Encode(actual))}
	}
	return nil
}

func substitute(value any, bound map[string]string) any {
	switch v := value.(type) {
	case lifecycle.Object:
		if name, ok := v["$use"].(string); ok && len(v) == 1 {
			return bound[name]
		}
		out := lifecycle.Object{}
		for k, x := range v {
			out[k] = substitute(x, bound)
		}
		return out
	case lifecycle.Array:
		out := lifecycle.Array{}
		for _, x := range v {
			out = append(out, substitute(x, bound))
		}
		return out
	}
	return value
}

// padded adds one unknown field so that the canonical payload is exactly
// pad["size"] bytes, as test_protocol.py does.
func padded(t *testing.T, request lifecycle.Object, pad lifecycle.Object) []byte {
	build := func(key string) []byte {
		out := lifecycle.Object{}
		for k, v := range request {
			out[k] = v
		}
		if pad["in"] == "body" {
			body := lifecycle.Object{key: ""}
			for k, v := range request["body"].(lifecycle.Object) {
				body[k] = v
			}
			out["body"] = body
		} else {
			out[key] = ""
		}
		raw, err := Canonical(out)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	prefix := pad["key_prefix"].(string)
	room := count(pad["size"]) - len(build(prefix))
	if room < 0 {
		t.Fatal("pad_to is smaller than the request")
	}
	return build(prefix + strings.Repeat("x", room))
}

// frames splits the output into decoded objects and exact payloads.
func frames(raw []byte) ([]any, [][]byte, error) {
	var objects []any
	var payloads [][]byte
	for len(raw) > 0 {
		if len(raw) < 4 {
			return nil, nil, fmt.Errorf("output ends inside a frame header")
		}
		size := int(binary.BigEndian.Uint32(raw))
		if len(raw)-4 < size {
			return nil, nil, fmt.Errorf("output ends inside a frame payload")
		}
		body := raw[4 : 4+size]
		value, err := lifecycle.Decode(body)
		if err != nil {
			return nil, nil, err
		}
		objects, payloads = append(objects, value), append(payloads, body)
		raw = raw[4+size:]
	}
	return objects, payloads, nil
}

func frameOf(payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	return append(out, payload...)
}

// shape checks a response against the envelope and, on success, against the
// result schema of its operation.
func shape(s *Schemas, response any, operation any) []string {
	obj, _ := response.(lifecycle.Object)
	env := s.Envelope
	if obj["status"] == "ok" {
		if err := s.Validate(env["success"].(lifecycle.Object), response, "response", env); err != nil {
			return []string{"response schema: " + err.Error()}
		}
		defs, known := s.Operations[fmt.Sprint(operation)]
		if !known {
			return []string{"response schema: a success needs a known operation"}
		}
		if err := s.Validate(defs["result"].(lifecycle.Object), obj["result"], "result", defs); err != nil {
			return []string{"result schema: " + err.Error()}
		}
		return nil
	}
	if err := s.Validate(env["failure"].(lifecycle.Object), response, "response", env); err != nil {
		return []string{"response schema: " + err.Error()}
	}
	return nil
}

func runStep(t *testing.T, a *Adapter, step lifecycle.Object, bound map[string]string) []string {
	expect := step["expect"].(lifecycle.Object)
	if text, ok := step["stream_hex"].(string); ok {
		data, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		var reader interface{ Read([]byte) (int, error) } = bytes.NewReader(data)
		if n, has := step["read_size"]; has {
			reader = &chunkReader{data: data, size: count(n)}
		}
		writer := &shortWriter{size: MaxFrame + 4}
		if n, has := step["write_size"]; has {
			writer.size = count(n)
		}
		if err := a.Serve(reader, writer); err != nil {
			return []string{"serve: " + err.Error()}
		}
		got, payloads, err := frames(writer.Bytes())
		if err != nil {
			return []string{err.Error()}
		}
		errs := match(expect["responses"], lifecycle.Array(got), bound, "responses")
		for i, r := range got {
			if canonical, err := Canonical(r); err != nil || !bytes.Equal(canonical, payloads[i]) {
				errs = append(errs, fmt.Sprintf("[%d]: payload is not canonical", i))
			}
			if len(payloads[i]) > MaxFrame {
				errs = append(errs, fmt.Sprintf("[%d]: response exceeds the frame bound", i))
			}
			errs = append(errs, shape(a.Schemas, r, step["operation"])...)
		}
		if _, has := step["write_size"]; has && writer.Len() > writer.size && writer.calls < 2 {
			errs = append(errs, "the short writer got only one write")
		}
		return errs
	}
	var payload []byte
	var operation any
	switch {
	case step["pad_to"] != nil:
		request := substitute(step["request"], bound).(lifecycle.Object)
		payload = padded(t, request, step["pad_to"].(lifecycle.Object))
		operation = request["operation"]
	case step["request"] != nil:
		request := substitute(step["request"], bound)
		raw, err := Canonical(request)
		if err != nil {
			t.Fatal(err)
		}
		payload, operation = raw, request.(lifecycle.Object)["operation"]
	case step["payload_text"] != nil:
		payload = []byte(step["payload_text"].(string))
	default:
		raw, err := hex.DecodeString(step["payload_hex"].(string))
		if err != nil {
			t.Fatal(err)
		}
		payload = raw
	}
	response, closing := a.Frame(payload)
	actual := normal(t, response)
	errs := match(expect["response"], actual, bound, "response")
	errs = append(errs, shape(a.Schemas, actual, operation)...)
	if want, _ := expect["close"].(bool); want != closing {
		errs = append(errs, fmt.Sprintf("close=%v", closing))
	}
	return errs
}

func TestSessionVectors(t *testing.T) {
	s := loadTestSchemas(t)
	paths, _ := filepath.Glob(filepath.Join(protocolDir, "vectors", "session-*.json"))
	if len(paths) == 0 {
		t.Fatal("no session vectors found")
	}
	total := 0
	for _, path := range paths {
		name := filepath.Base(path)
		doc := readVector(t, name)
		t.Run(name, func(t *testing.T) {
			store, err := lifecycle.OpenGitStore(filepath.Join(t.TempDir(), "store.git"))
			if err != nil {
				t.Fatal(err)
			}
			host, err := lifecycle.NewHost(store, noRuntime{}, doc["context"])
			if err != nil {
				t.Fatal(err)
			}
			a := NewAdapter(host, s)
			bound := map[string]string{}
			for _, raw := range doc["steps"].(lifecycle.Array) {
				step := raw.(lifecycle.Object)
				total++
				for _, e := range runStep(t, a, step, bound) {
					t.Errorf("%s: %s", step["name"], e)
				}
			}
		})
	}
	if total < 61 {
		t.Fatalf("ran %d session steps; the vector files have at least 61", total)
	}
}

func TestJCSVectors(t *testing.T) {
	for _, raw := range readVector(t, "jcs.json")["cases"].(lifecycle.Array) {
		c := raw.(lifecycle.Object)
		value, err := lifecycle.Decode([]byte(c["input"].(string)))
		if err != nil {
			t.Fatalf("%s: %v", c["name"], err)
		}
		got, err := Canonical(value)
		if err != nil || string(got) != c["canonical"] {
			t.Errorf("%s: got %q, %v", c["name"], got, err)
		}
	}
}

func TestEnvelopeSchemaVectors(t *testing.T) {
	s := loadTestSchemas(t)
	cases := readVector(t, "envelope-schema.json")["cases"].(lifecycle.Array)
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, raw := range cases {
		c := raw.(lifecycle.Object)
		def := s.Envelope[c["def"].(string)].(lifecycle.Object)
		err := s.Validate(def, c["value"], c["def"].(string), s.Envelope)
		if (err == nil) != c["valid"].(bool) {
			t.Errorf("%s: valid=%v (%v)", c["name"], err == nil, err)
		}
	}
}
