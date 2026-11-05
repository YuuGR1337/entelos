package lifecycle

// The wire protocol has one schema file for each Host operation. The fields
// of each request body agree with the Host table. A write operation also has
// expected_revision and request_key. Each schema pattern starts with ^ and
// ends with $.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func names(m Object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkPatterns(t *testing.T, node any, where string) {
	switch v := node.(type) {
	case Object:
		for k, x := range v {
			if p, ok := x.(string); ok && k == "pattern" && !(strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$")) {
				t.Errorf("%s: pattern %q must start with ^ and end with $", where, p)
			}
			checkPatterns(t, x, where)
		}
	case Array:
		for _, x := range v {
			checkPatterns(t, x, where)
		}
	}
}

func TestProtocolSchemasAgreeWithTheHostTable(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "protocol", "schema", "*.schema.json"))
	files := Object{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".schema.json")
		checkPatterns(t, doc, name)
		if name != "envelope" {
			files[name] = doc
		}
	}
	want := make([]string, 0, len(schema))
	for op := range schema {
		want = append(want, op)
	}
	sort.Strings(want)
	if got := names(files); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schema files %v, Host operations %v", got, want)
	}
	for op, entry := range schema {
		doc := files[op].(Object)
		body := at(doc, "$defs", "request_body").(Object)
		fields := append([]string{}, entry.fields...)
		if !Reads[op] {
			fields = append(fields, "expected_revision", "request_key")
		}
		sort.Strings(fields)
		if got := names(body["properties"].(Object)); strings.Join(got, ",") != strings.Join(fields, ",") {
			t.Errorf("%s: schema fields %v, Host fields %v", op, got, fields)
		}
		for _, x := range body["required"].(Array) {
			if !withinStrings(x, fields) {
				t.Errorf("%s: required field %v is not in the Host table", op, x)
			}
		}
		if (doc["x-entelos-access"] == "read") != Reads[op] {
			t.Errorf("%s: x-entelos-access does not agree with the Host read operations", op)
		}
	}
}
