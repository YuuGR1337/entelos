package lifecycle

// Value helpers keep the request semantics of the lifecycle Host.
// A missing field or a wrong JSON type is a requestError. Host.Handle turns
// it into an invalid_request diagnostic.

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Fault is a structured lifecycle diagnostic.
type Fault struct {
	Code, Message, Stage string
}

// NewFault makes a Fault. An empty stage is the lifecycle stage.
func NewFault(code, message, stage string) *Fault {
	if stage == "" {
		stage = "lifecycle"
	}
	return &Fault{Code: code, Message: message, Stage: stage}
}

func (f *Fault) Error() string { return f.Code + ": " + f.Message }

// Record gives the wire form of the diagnostic.
func (f *Fault) Record() Object {
	return Object{"status": f.Code, "diagnostic": Object{"code": f.Code, "stage": f.Stage, "message": f.Message}}
}

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }

func fail(code, message, stage string) { panic(NewFault(code, message, stage)) }

func keyError(key string) { panic(&requestError{"'" + key + "'"}) }

func typeError(format string, args ...any) { panic(&requestError{fmt.Sprintf(format, args...)}) }

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case Int, int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case Array:
		return "list"
	case Object:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// item is value[key] for a JSON object.
func item(value any, key string) any {
	m, ok := value.(Object)
	if !ok {
		typeError("'%s' object is not subscriptable by str", typeName(value))
	}
	v, ok := m[key]
	if !ok {
		keyError(key)
	}
	return v
}

// lookup is value.get(key, fallback) for a JSON object.
func lookup(value any, key string, fallback any) any {
	m, ok := value.(Object)
	if !ok {
		typeError("'%s' object has no attribute 'get'", typeName(value))
	}
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}

func object(value any) Object {
	m, ok := value.(Object)
	if !ok {
		typeError("expected an object, got %s", typeName(value))
	}
	return m
}

func text(value any) string {
	s, ok := value.(string)
	if !ok {
		typeError("expected a string, got %s", typeName(value))
	}
	return s
}

func hashable(value any) any {
	switch value.(type) {
	case Array, Object:
		typeError("unhashable type: '%s'", typeName(value))
	}
	return value
}

// truthy is Python truth value testing.
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case Int:
		return v.Text != "0"
	case int:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case Array:
		return len(v) > 0
	case Object:
		return len(v) > 0
	}
	return true
}

// elements is Python iteration over a JSON value.
func elements(value any) Array {
	switch v := value.(type) {
	case Array:
		return v
	case Object:
		out := make(Array, 0, len(v))
		for _, k := range sortedKeys(v) {
			out = append(out, k)
		}
		return out
	case string:
		out := make(Array, 0, utf8.RuneCountInString(v))
		for _, r := range v {
			out = append(out, string(r))
		}
		return out
	}
	typeError("'%s' object is not iterable", typeName(value))
	return nil
}

// within is the Python "needle in container" test.
func within(needle, container any) bool {
	switch c := container.(type) {
	case Array:
		for _, x := range c {
			if equal(x, needle) {
				return true
			}
		}
		return false
	case Object:
		key, ok := hashable(needle).(string)
		if !ok {
			return false
		}
		_, found := c[key]
		return found
	case string:
		s, ok := needle.(string)
		if !ok {
			typeError("'in <string>' requires string as left operand, not %s", typeName(needle))
		}
		return strings.Contains(c, s)
	}
	typeError("argument of type '%s' is not iterable", typeName(container))
	return false
}

func withinStrings(needle any, list []string) bool {
	for _, x := range list {
		if equal(x, needle) {
			return true
		}
	}
	return false
}

// less is a < b for two strings.
func less(a, b any) bool {
	x, ok1 := a.(string)
	y, ok2 := b.(string)
	if !ok1 || !ok2 {
		typeError("'<' not supported between instances of '%s' and '%s'", typeName(a), typeName(b))
	}
	return x < y
}

func lessEqual(a, b any) bool { return !less(b, a) }

func copyObject(m Object) Object {
	out := make(Object, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// merge is the Python "a | b" for objects: a copy of a, updated with b.
func merge(a, b Object) Object {
	out := copyObject(a)
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sortedKeys(m Object) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stringList is the Python list(...) of strings in a state field.
func stringList(value any) Array {
	a, ok := value.(Array)
	if !ok {
		typeError("expected a list, got %s", typeName(value))
	}
	return a
}

// pyLen is len(value) for an iterable JSON value.
func pyLen(value any) int {
	switch v := value.(type) {
	case Array:
		return len(v)
	case Object:
		return len(v)
	case string:
		return utf8.RuneCountInString(v)
	}
	typeError("object of type '%s' has no len()", typeName(value))
	return 0
}
