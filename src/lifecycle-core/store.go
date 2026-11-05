package lifecycle

// The storage port. The Host uses only Read, Transact and History, and the
// read-view operations. See src/cel-lifecycle/storage-port.md.

import (
	"bytes"
	"regexp"
	"unicode/utf8"
)

// Store is the storage port of the lifecycle core.
type Store interface {
	// Read gives an immutable view at a cursor. A nil cursor is the current
	// accepted root. An empty cursor is the empty state. A cursor that is not
	// a string gives unknown_cursor.
	Read(cursor any) (*View, error)
	// Transact runs apply on the current view and publishes the result
	// atomically, only if the root is still the expected revision. A
	// request key that already succeeded gives its original result.
	Transact(expected, key, payload any, apply func(*View) (any, error)) (Object, error)
	// History lists the accepted first-parent cursors, newest first.
	History(cursor string) ([]string, error)
}

// objectSource reads committed object bodies by their store handle.
type objectSource interface {
	object(handle string) ([]byte, error)
}

// View is a captured state at one cursor, plus the objects that the current
// transaction stages.
type View struct {
	Cursor  string
	State   Object
	Entries map[string]string // object name to store handle
	pending map[string][]byte
	source  objectSource
}

var refPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// RecordFormat is the format of each record that this core writes. The
// number after the slash is the major version.
const RecordFormat = "entelos.record/1"

var formatPattern = regexp.MustCompile(`^entelos\.record/([1-9][0-9]{0,8})$`)

func newView(source objectSource, cursor string, state Object, entries map[string]string) *View {
	return &View{Cursor: cursor, State: state, Entries: entries, pending: map[string][]byte{}, source: source}
}

// PutRaw stages raw bytes with a media type and gives their content reference.
func (v *View) PutRaw(mime string, raw []byte) string {
	body := Envelope(mime, raw)
	ref := ContentRef(body)
	v.pending[ref] = body
	return ref
}

// Put stages a record of a kind. If the value is an object, Put first sets
// its format field to RecordFormat.
func (v *View) Put(kind string, value any) string {
	if record, ok := value.(Object); ok {
		record["format"] = RecordFormat
	}
	return v.PutRaw(Media(kind), Encode(value))
}

// GetRaw reads verified bytes by exact content reference. An empty mime
// skips the media type check.
func (v *View) GetRaw(ref any, mime string) ([]byte, error) {
	s, ok := ref.(string)
	if !ok || !refPattern.MatchString(s) {
		return nil, NewFault("invalid_reference", "expected an exact content reference", "store")
	}
	body, ok := v.pending[s]
	if ok {
		body = bytes.Clone(body)
	} else {
		handle, found := v.Entries["o-"+s[7:]]
		if !found {
			return nil, NewFault("artifact_unavailable", s, "store")
		}
		var err error
		if body, err = v.source.object(handle); err != nil {
			return nil, err
		}
	}
	if ContentRef(body) != s || !bytes.Contains(body, []byte{0}) {
		return nil, NewFault("artifact_mismatch", "content bytes differ from reference", "store")
	}
	actual, raw, _ := bytes.Cut(body, []byte{0})
	if mime != "" {
		if !utf8.Valid(actual) {
			return nil, &requestError{"media type is not UTF-8"}
		}
		if string(actual) != mime {
			return nil, NewFault("invalid_record", "unexpected media type", "store")
		}
	}
	return raw, nil
}

// Get reads a record object of a kind.
func (v *View) Get(ref any, kind string) (Object, error) {
	raw, err := v.GetRaw(ref, Media(kind))
	if err != nil {
		return nil, err
	}
	value, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	m, ok := value.(Object)
	if !ok {
		return nil, NewFault("invalid_record", "record must be an object", "store")
	}
	if err := checkFormat(m); err != nil {
		return nil, err
	}
	return m, nil
}

// checkFormat stops a read of a record that this core cannot read. A record
// without a format, or with a different major version, gives
// unsupported_record_format. A format value of a different shape gives
// invalid_record.
func checkFormat(record Object) error {
	value, has := record["format"]
	if !has {
		return NewFault("unsupported_record_format", "record has no format field; this core reads only "+RecordFormat, "store")
	}
	s, ok := value.(string)
	match := formatPattern.FindStringSubmatch(s)
	if !ok || match == nil {
		return NewFault("invalid_record", "record format is malformed", "store")
	}
	if s != RecordFormat {
		return NewFault("unsupported_record_format", "record format major version "+match[1]+" is unknown; this core reads only "+RecordFormat, "store")
	}
	return nil
}

// cursorAt gives the cursor to read. nil is the current root.
func cursorAt(cursor any, root string) (string, error) {
	if cursor == nil {
		return root, nil
	}
	at, ok := cursor.(string)
	if !ok {
		return "", NewFault("unknown_cursor", "cursor is not in accepted history", "store")
	}
	return at, nil
}

// EmptyState is the state before the first accepted transaction.
func EmptyState() Object {
	return Object{"schema": "local-lifecycle/v1", "heads": Object{}, "sources": Array{}, "plans": Array{}, "events": Array{},
		"requests": Object{}, "workers": Object{}, "receipts": Array{}, "tasks": Object{}, "outbox": Object{}}
}

// checkKey is the bounded request key rule.
func checkKey(key any) (string, error) {
	s, ok := key.(string)
	if !ok || s == "" || utf8.RuneCountInString(s) > 128 {
		return "", NewFault("invalid_request", "bounded request key required", "")
	}
	return s, nil
}

// requestDigest is the identity of an idempotent request payload.
func requestDigest(payload any) string {
	return ContentRef(Envelope(Media("request"), Encode(payload)))
}

// reuse finds an earlier success of a request key. found is false if the key
// has no earlier success.
func reuse(store Store, view *View, key, digest string) (Object, bool, error) {
	requests, ok := view.State["requests"].(Object)
	if !ok {
		return nil, false, NewFault("invalid_store", "missing requests", "store")
	}
	previous, ok := requests[key].(Object)
	if !ok || !truthy(previous) {
		return nil, false, nil
	}
	if !equal(previous["payload_ref"], digest) {
		return nil, true, NewFault("idempotency_conflict", "key was used for another payload", "")
	}
	// A root cannot contain its own ID. Find the first accepted root that contains this success.
	origin := view.Cursor
	history, err := store.History(view.Cursor)
	if err != nil {
		return nil, true, err
	}
	for _, cursor := range history {
		c := cursor
		older, err := store.Read(c)
		if err != nil {
			return nil, true, err
		}
		requests, ok := older.State["requests"].(Object)
		if !ok {
			return nil, true, NewFault("invalid_store", "missing requests", "store")
		}
		if _, has := requests[key]; !has {
			break
		}
		origin = cursor
	}
	return Object{"cursor": origin, "result": previous["result"], "reused": true}, true, nil
}
