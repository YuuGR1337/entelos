package lifecycle

// A Go harness for the storage port vectors in
// src/cel-lifecycle/conformance/storage. It follows test_storage_port.py and
// runs each vector on a fresh Git store and a fresh in-memory store.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var vectorInvariants = []string{"atomic_publication", "idempotent_success", "immutable_content_identity", "known_record_format",
	"no_partial_state_after_failure", "stable_historical_read", "stale_cas_rejection"}

type vectorRun struct {
	store      Store
	names      map[string]string
	mismatches []string
}

// vectorAbort stops a vector on a harness error, like an exception in Python.
type vectorAbort struct{ message string }

func (r *vectorRun) abort(format string, args ...any) {
	panic(vectorAbort{fmt.Sprintf(format, args...)})
}

func (r *vectorRun) value(expr any) any {
	if s, ok := expr.(string); ok && strings.HasPrefix(s, "$") {
		cursor, found := r.names[s[1:]]
		if !found {
			r.abort("unbound cursor name %s", s)
		}
		return cursor
	}
	return expr
}

func (r *vectorRun) same(where string, actual, expected any) {
	if !equal(actual, expected) {
		r.mismatches = append(r.mismatches, fmt.Sprintf("%s: expected %s, actual %s", where, Encode(expected), Encode(actual)))
	}
}

func (r *vectorRun) error(where string, expect Object, err error) {
	var f *Fault
	got := "unexpected:" + fmt.Sprint(err)
	if errors.As(err, &f) {
		got = f.Code
	}
	if _, has := expect["error"]; !has {
		r.mismatches = append(r.mismatches, fmt.Sprintf("%s: expected %s, actual error %s", where, Encode(expect), got))
		return
	}
	r.same(where+".error", got, expect["error"])
}

func (r *vectorRun) bind(where, name string, cursor string) {
	reused := false
	for _, x := range r.names {
		reused = reused || x == cursor
	}
	if cursor == "" || reused {
		r.mismatches = append(r.mismatches, where+": expected a new nonempty cursor, actual "+cursor)
	}
	r.names[name] = cursor
}

func (r *vectorRun) actions(where string, view *View, actions any) error {
	for i, raw := range actions.(Array) {
		at := fmt.Sprintf("%s.actions[%d]", where, i)
		action := raw.(Object)
		switch {
		case action["raise"] != nil:
			return NewFault(action["raise"].(string), "vector apply failure", "vector")
		case action["cursor_is"] != nil:
			r.same(at+".cursor", view.Cursor, r.value(action["cursor_is"]))
		case action["set"] != nil:
			path := action["set"].(Array)
			target := view.State
			for _, name := range path[:len(path)-1] {
				next, ok := target[name.(string)].(Object)
				if !ok {
					next = Object{}
					target[name.(string)] = next
				}
				target = next
			}
			target[path[len(path)-1].(string)] = action["value"]
		case action["put"] != nil:
			r.same(at+".ref", view.Put(action["put"].(string), action["value"]), action["expect_ref"])
		case action["put_raw"] != nil:
			r.same(at+".ref", view.PutRaw(action["put_raw"].(string), []byte(action["text"].(string))), action["expect_ref"])
		case action["get"] != nil || action["get_raw"] != nil:
			var actual Object
			var err error
			if action["get"] != nil {
				var value Object
				value, err = view.Get(action["get"], action["kind"].(string))
				actual = Object{"value": value}
			} else {
				mime, _ := action["mime"].(string)
				var raw []byte
				raw, err = view.GetRaw(action["get_raw"], mime)
				actual = Object{"text": string(raw)}
			}
			if err != nil {
				r.error(at, action["expect"].(Object), err)
			} else {
				r.same(at, actual, action["expect"])
			}
		case action["transact"] != nil:
			r.step(at+".transact", action["transact"].(Object))
		default:
			r.abort("unknown action: %s", strings.Join(sortedKeys(action), ","))
		}
	}
	return nil
}

func (r *vectorRun) checkView(where string, view *View, expect Object) {
	if c, has := expect["cursor"]; has {
		r.same(where+".cursor", view.Cursor, r.value(c))
	}
	if s, has := expect["state"]; has {
		r.same(where+".state", view.State, s)
	}
	if includes, ok := expect["state_includes"].(Object); ok {
		for name, value := range includes {
			r.same(where+".state."+name, view.State[name], value)
		}
	}
}

func (r *vectorRun) step(where string, step Object) {
	expect := step["expect"].(Object)
	_, wantError := expect["error"]
	switch step["op"] {
	case "read":
		var cursor any
		if c, has := step["cursor"]; has {
			cursor = r.value(c)
		}
		view, err := r.store.Read(cursor)
		if err != nil {
			r.error(where, expect, err)
			return
		}
		if wantError {
			r.mismatches = append(r.mismatches, where+": expected an error, actual cursor "+view.Cursor)
			return
		}
		r.checkView(where, view, expect)
		if actions, has := step["actions"]; has {
			if err := r.actions(where, view, actions); err != nil {
				r.abort("%v", err)
			}
		}
	case "history":
		cursors, err := r.store.History(r.value(step["cursor"]).(string))
		if err != nil {
			r.abort("%v", err)
		}
		want := Array{}
		for _, x := range expect["cursors"].(Array) {
			want = append(want, r.value(x))
		}
		got := Array{}
		for _, x := range cursors {
			got = append(got, x)
		}
		r.same(where+".cursors", got, want)
	case "transact":
		calls := 0
		apply := step["apply"].(Object)
		outcome, err := r.store.Transact(r.value(step["expected"]), step["key"], step["payload"], func(v *View) (any, error) {
			calls++
			if err := r.actions(where+".apply", v, apply["actions"]); err != nil {
				return nil, err
			}
			return apply["result"], nil
		})
		switch {
		case err != nil:
			r.error(where, expect, err)
		case wantError:
			r.mismatches = append(r.mismatches, fmt.Sprintf("%s: expected %s, actual %s", where, Encode(expect), Encode(outcome)))
		default:
			if name, ok := expect["bind"].(string); ok {
				r.bind(where+".cursor", name, outcome["cursor"].(string))
			}
			if c, has := expect["cursor"]; has {
				r.same(where+".cursor", outcome["cursor"], r.value(c))
			}
			r.same(where+".result", outcome["result"], expect["result"])
			r.same(where+".reused", outcome["reused"], expect["reused"])
		}
		if n, has := expect["apply_calls"]; has {
			r.same(where+".apply_calls", Int{fmt.Sprint(calls)}, n)
		}
	default:
		r.abort("unknown step op: %v", step["op"])
	}
}

func runVector(store Store, vector Object) (mismatches []string) {
	r := &vectorRun{store: store, names: map[string]string{}}
	defer func() {
		if p := recover(); p != nil {
			mismatches = append(r.mismatches, fmt.Sprintf("harness: %v", p))
		}
	}()
	for i, step := range vector["steps"].(Array) {
		r.step(fmt.Sprintf("steps[%d]", i), step.(Object))
	}
	return r.mismatches
}

func TestStoragePortVectors(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "cel-lifecycle", "conformance", "storage", "*.json"))
	if len(paths) == 0 {
		t.Fatal("no storage port vectors found")
	}
	covered := map[string]bool{}
	var vectors []Object
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		vector := value.(Object)
		if vector["format"] != "storage-port-vector/v1" {
			t.Fatalf("%s: unknown vector format", path)
		}
		for _, x := range vector["invariants"].(Array) {
			covered[x.(string)] = true
		}
		vectors = append(vectors, vector)
	}
	names := make([]string, 0, len(covered))
	for name := range covered {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(vectorInvariants, ",") {
		t.Fatalf("vectors cover %v; the spec names %v", names, vectorInvariants)
	}
	for _, vector := range vectors {
		id := vector["id"].(string)
		t.Run("git/"+id, func(t *testing.T) {
			store, err := OpenGitStore(filepath.Join(t.TempDir(), id+".git"))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range runVector(store, vector) {
				t.Error(m)
			}
		})
		t.Run("memory/"+id, func(t *testing.T) {
			for _, m := range runVector(NewMemoryStore(), vector) {
				t.Error(m)
			}
		})
	}
}

// A harness abort keeps the mismatches that came before it.
func TestVectorAbortKeepsEarlierMismatches(t *testing.T) {
	got := runVector(NewMemoryStore(), Object{"steps": Array{
		Object{"op": "read", "expect": Object{"cursor": "not-empty"}},
		Object{"op": "unknown", "expect": Object{}},
	}})
	if len(got) != 2 || !strings.HasPrefix(got[0], "steps[0].cursor") || !strings.HasPrefix(got[1], "harness:") {
		t.Fatalf("got %q", got)
	}
}
