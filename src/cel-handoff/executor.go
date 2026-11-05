package handoff

// An executor runs one pinned activation and writes log entries for ingest.
// It reads no clock, file, process or socket. The embedding host supplies
// the date, the log cursor and the log storage. Thus the same code runs in a
// native process, in a gomobile package and in a browser as WebAssembly.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Pin is the activation that an embedding host gives to an executor. The
// host takes the fields from the lifecycle records: the enable event gives
// function, scope, from, until and snapshot_ref; the snapshot gives
// bundle_ref, engine_build and stale_mode. Revoked is true if the host knows
// a later event that masks the activation. The executor does not verify these
// claims. A content reference is an identity, not a signature.
type Pin struct {
	Function    string `json:"function"`
	Scope       string `json:"scope"`
	SnapshotRef string `json:"snapshot_ref"`
	BundleRef   string `json:"bundle_ref"`
	EngineBuild string `json:"engine_build"`
	From        string `json:"from"`
	Until       string `json:"until"`
	StaleMode   string `json:"stale_mode"`
	Revoked     *bool  `json:"revoked"`
}

// Entry is one log entry in the format of the lifecycle ingest operation.
// It has no raw inputs. The embedding host keeps the inputs and uploads them
// only if its upload policy permits it.
type Entry struct {
	Cursor      int64    `json:"cursor"`
	SnapshotRef string   `json:"snapshot_ref"`
	Date        string   `json:"date"`
	InputsRef   string   `json:"inputs_ref"`
	Response    Response `json:"response"`
	StaleReason string   `json:"stale_reason"`
}

// Executor holds one loaded bundle and its pin.
type Executor struct {
	bundle *Bundle
	pin    Pin
}

// maxCursor is 2^53-1, the largest integer that JSON readers keep exactly.
const maxCursor = 1<<53 - 1

var contentRef = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// NewExecutor loads the bundle and checks the pin against it. The pin must
// name the loaded bundle and the build of this executor, so a bundle that
// qualification passed on another build fails closed.
func NewExecutor(pinJSON []byte, f Files) (*Executor, *Diagnostic) {
	var pin Pin
	if err := strictJSON(pinJSON, &pin); err != nil {
		return nil, diag("invalid_pin", "pin", "", err.Error())
	}
	b, d := Load(f)
	if d != nil {
		return nil, d
	}
	// The checks run in a fixed order, so one pin always gives the same diagnostic.
	for _, c := range []struct{ field, value string }{{"/function", pin.Function}, {"/scope", pin.Scope}} {
		if c.value == "" {
			return nil, diag("invalid_pin", "pin", c.field, "a nonempty string is required")
		}
	}
	for _, c := range []struct{ field, value string }{{"/snapshot_ref", pin.SnapshotRef}, {"/bundle_ref", pin.BundleRef}, {"/engine_build", pin.EngineBuild}} {
		if !contentRef.MatchString(c.value) {
			return nil, diag("invalid_pin", "pin", c.field, "a sha256 content reference is required")
		}
	}
	if pin.Revoked == nil {
		return nil, diag("invalid_pin", "pin", "/revoked", "revoked must be true or false")
	}
	if pin.StaleMode != "fail_closed" && pin.StaleMode != "continue_stale" {
		return nil, diag("invalid_pin", "pin", "/stale_mode", "stale_mode must be fail_closed or continue_stale")
	}
	if err := checkDate(pin.From); err != nil {
		return nil, diag("invalid_pin", "pin", "/from", err.Error())
	}
	if err := checkDate(pin.Until); err != nil {
		return nil, diag("invalid_pin", "pin", "/until", err.Error())
	}
	if pin.From >= pin.Until {
		return nil, diag("invalid_pin", "pin", "/until", "until must be after from")
	}
	if pin.Function != b.manifest.FunctionID {
		return nil, diag("invalid_pin", "pin", "/function", "the pin names another function than the bundle")
	}
	if pin.BundleRef != b.ref {
		return nil, diag("artifact_mismatch", "pin", "/bundle_ref", "the pin names another bundle")
	}
	if pin.EngineBuild != BuildID() {
		return nil, diag("implementation_unavailable", "pin", "/engine_build", "the pinned build is not this executor build")
	}
	return &Executor{bundle: b, pin: pin}, nil
}

// Describe gives the callable descriptor of the loaded bundle.
func (e *Executor) Describe() Descriptor { return e.bundle.Describe() }

// Invoke evaluates the inputs at the date and gives the log entry at the
// cursor. A refusal gives a diagnostic and no entry, so the host writes
// nothing to its log. An answer of the engine, also a failed one such as
// needs_input, gives an entry.
func (e *Executor) Invoke(ctx context.Context, cursor int64, date string, inputs []byte) (*Entry, *Diagnostic) {
	if cursor < 1 || cursor > maxCursor {
		return nil, diag("invalid_request", "log", "/cursor", "cursor must be an integer from 1 to 2^53-1")
	}
	if err := checkDate(date); err != nil {
		return nil, diag("invalid_interval", "request", "/date", err.Error())
	}
	if len(inputs) > e.bundle.manifest.Limits.MaxRequestBytes {
		return nil, diag("invalid_input", "input", "/inputs", "inputs exceed the request byte limit")
	}
	var values map[string]any
	if err := strictJSON(inputs, &values); err != nil {
		return nil, diag("invalid_input", "input", "/inputs", err.Error())
	}
	if values == nil {
		return nil, diag("invalid_input", "input", "/inputs", "inputs must be a non-null JSON object")
	}
	canonical, err := canonicalInputs(values)
	if err != nil {
		return nil, diag("invalid_input", "input", "/inputs", err.Error())
	}
	reason := ""
	switch {
	case date < e.pin.From:
		return nil, diag("out_of_scope", "resolve", "/date", "the date is before the pinned activation")
	case date >= e.pin.Until:
		reason = "expired"
	case *e.pin.Revoked:
		reason = "revoked"
	}
	if reason != "" && e.pin.StaleMode != "continue_stale" {
		return nil, diag("activation_"+reason, "resolve", "/date", "the pinned activation is "+reason+" at this date; the revision fails closed")
	}
	// The request has the bytes that the lifecycle Host sends to the engine,
	// so the request byte limit applies the same way.
	request := append([]byte(`{"bundle_ref":"`+e.bundle.ref+`","inputs":`), canonical...)
	request = append(request, "}\n"...)
	return &Entry{Cursor: cursor, SnapshotRef: e.pin.SnapshotRef, Date: date,
		InputsRef: Digest("application/json", append(canonical, '\n')), Response: e.bundle.Run(ctx, request), StaleReason: reason}, nil
}

// MarshalEntry gives the JSON bytes of an entry, without HTML escapes.
func MarshalEntry(entry *Entry) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entry); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// checkDate accepts only a canonical ISO date, as the lifecycle Host does.
func checkDate(s string) error {
	parsed, err := time.Parse("2006-01-02", s)
	if err != nil {
		return err
	}
	if parsed.Year() < 1 || parsed.Format("2006-01-02") != s {
		return fmt.Errorf("date must be a canonical ISO date")
	}
	return nil
}

// canonicalInputs gives the lifecycle record bytes of an inputs object,
// without the final newline: sorted keys, compact separators, raw UTF-8 and
// short escapes. The executor accepts only string, boolean and null values.
// Other values are outside the string/bool profile, and their canonical
// number text is not part of this executor.
func canonicalInputs(values map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(&b, k)
		b.WriteByte(':')
		switch v := values[k].(type) {
		case nil:
			b.WriteString("null")
		case bool:
			fmt.Fprint(&b, v)
		case string:
			writeString(&b, v)
		default:
			return nil, fmt.Errorf("input %s must be a string, a boolean or null", pointer(k))
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
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
