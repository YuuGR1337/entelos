package protocol

// A small JSON Schema 2020-12 validator. It supports only the keywords that
// the files in src/protocol/schema use, the same as the Python reference
// adapter.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	lifecycle "example.org/lifecycle-core"
)

// Schemas holds the envelope definitions and one schema for each operation.
type Schemas struct {
	Envelope   lifecycle.Object            // the $defs of envelope.schema.json
	Operations map[string]lifecycle.Object // the $defs of each <operation>.schema.json
	patterns   map[string]*regexp.Regexp
}

// requestError is a protocol failure before the Host gets the request.
type requestError struct{ code, message string }

func (e *requestError) Error() string { return e.code + ": " + e.message }

func invalid(format string, args ...any) *requestError {
	return &requestError{"invalid_request", fmt.Sprintf(format, args...)}
}

// LoadSchemas reads each *.schema.json file in dir. Each operation schema must
// have a request body, a result and an access type that agrees with the Host
// read operations.
func LoadSchemas(dir string) (*Schemas, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.schema.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s: no *.schema.json files", dir)
	}
	s := &Schemas{Operations: map[string]lifecycle.Object{}, patterns: map[string]*regexp.Regexp{}}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		value, err := lifecycle.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		doc, ok := value.(lifecycle.Object)
		defs, hasDefs := doc["$defs"].(lifecycle.Object)
		if !ok || !hasDefs {
			return nil, fmt.Errorf("%s: no $defs object", path)
		}
		if err := s.compile(doc, path); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".schema.json")
		if name == "envelope" {
			s.Envelope = defs
			continue
		}
		_, hasBody := defs["request_body"].(lifecycle.Object)
		_, hasResult := defs["result"].(lifecycle.Object)
		if !hasBody || !hasResult {
			return nil, fmt.Errorf("%s: request_body and result are required", path)
		}
		access := doc["x-entelos-access"]
		if (access == "read") != lifecycle.Reads[name] || (access != "read" && access != "write") {
			return nil, fmt.Errorf("%s: x-entelos-access does not agree with the Host read operations", path)
		}
		s.Operations[name] = defs
	}
	for _, name := range []string{"version", "request", "success", "failure", "diagnostic"} {
		if _, ok := s.Envelope[name].(lifecycle.Object); !ok {
			return nil, fmt.Errorf("%s: envelope definition %q is missing", dir, name)
		}
	}
	if len(s.Operations) == 0 {
		return nil, fmt.Errorf("%s: no operation schema", dir)
	}
	return s, nil
}

// compile prepares each pattern. A pattern must match the full string, as in
// ECMA-262, so a trailing newline does not match "$".
func (s *Schemas) compile(node any, where string) error {
	switch v := node.(type) {
	case lifecycle.Object:
		for k, x := range v {
			if p, ok := x.(string); ok && k == "pattern" {
				if !strings.HasPrefix(p, "^") || !strings.HasSuffix(p, "$") {
					return fmt.Errorf("%s: pattern %q must start with ^ and end with $", where, p)
				}
				re, err := regexp.Compile(`\A(?:` + p[1:len(p)-1] + `)\z`)
				if err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
				s.patterns[p] = re
				continue
			}
			if err := s.compile(x, where); err != nil {
				return err
			}
		}
	case lifecycle.Array:
		for _, x := range v {
			if err := s.compile(x, where); err != nil {
				return err
			}
		}
	}
	return nil
}

func count(v any) int {
	switch n := v.(type) {
	case lifecycle.Int:
		i, err := strconv.Atoi(n.Text)
		if err == nil {
			return i
		}
	case int:
		return n
	}
	return 0
}

func sameValue(a, b any) bool {
	x, err1 := Canonical(a)
	y, err2 := Canonical(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func isInteger(v any) bool {
	switch v.(type) {
	case lifecycle.Int, int, int64:
		return true
	}
	return false
}

// Validate checks value against schema. root holds the $defs for "$ref".
func (s *Schemas) Validate(schema lifecycle.Object, value any, where string, root lifecycle.Object) error {
	if ref, ok := schema["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/$defs/")
		target, isObject := root[name].(lifecycle.Object)
		if !found || !isObject {
			return fmt.Errorf("unsupported $ref: %s", ref)
		}
		return s.Validate(target, value, where, root)
	}
	if kind, ok := schema["type"].(string); ok {
		var match bool
		switch kind {
		case "object":
			_, match = value.(lifecycle.Object)
		case "array":
			_, match = value.(lifecycle.Array)
		case "string":
			_, match = value.(string)
		case "boolean":
			_, match = value.(bool)
		case "integer":
			match = isInteger(value)
		default:
			return fmt.Errorf("unsupported type: %s", kind)
		}
		if !match {
			return invalid("%s must be of type %s", where, kind)
		}
	}
	if c, ok := schema["const"]; ok && !sameValue(value, c) {
		return invalid("%s must be %s", where, lifecycle.Encode(c))
	}
	if list, ok := schema["enum"].(lifecycle.Array); ok {
		found := false
		for _, x := range list {
			found = found || sameValue(value, x)
		}
		if !found {
			return invalid("%s is not an allowed value", where)
		}
	}
	if str, ok := value.(string); ok {
		// maxLength and minLength count Unicode code points.
		n := utf8.RuneCountInString(str)
		if n < count(schema["minLength"]) {
			return invalid("%s is too short", where)
		}
		if limit, has := schema["maxLength"]; has && n > count(limit) {
			return invalid("%s is too long", where)
		}
		if p, has := schema["pattern"].(string); has && !s.patterns[p].MatchString(str) {
			return invalid("%s does not match its pattern", where)
		}
	}
	if list, ok := value.(lifecycle.Array); ok {
		if len(list) < count(schema["minItems"]) {
			return invalid("%s has too few items", where)
		}
		if items, has := schema["items"].(lifecycle.Object); has {
			for i, item := range list {
				if err := s.Validate(items, item, fmt.Sprintf("%s[%d]", where, i), root); err != nil {
					return err
				}
			}
		}
	}
	if obj, ok := value.(lifecycle.Object); ok {
		if required, has := schema["required"].(lifecycle.Array); has {
			for _, name := range required {
				if _, present := obj[name.(string)]; !present {
					return invalid("missing field: %s.%s", where, name)
				}
			}
		}
		props, _ := schema["properties"].(lifecycle.Object)
		extra, hasExtra := schema["additionalProperties"]
		names := make([]string, 0, len(obj))
		for name := range obj {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if prop, known := props[name].(lifecycle.Object); known {
				if err := s.Validate(prop, obj[name], where+"."+name, root); err != nil {
					return err
				}
			} else if hasExtra && extra == false {
				return invalid("unexpected field: %s.%s", where, name)
			} else if more, isSchema := extra.(lifecycle.Object); isSchema {
				if err := s.Validate(more, obj[name], where+"."+name, root); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
