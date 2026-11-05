package handoff_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	h "example.org/cel-handoff"
)

// The executor vectors are language neutral. This test runs them on the
// build that runs the test: a native build or WebAssembly under Node. If
// EXECUTOR_VECTOR_RESULTS names a file, the test writes each actual result
// there, so that a check can compare two builds.

const vectorDir = "../cel-lifecycle/conformance/executor"

type vectorDiag struct {
	Code  string `json:"code"`
	Stage string `json:"stage"`
	Field string `json:"field"`
}

type vector struct {
	Format string `json:"format"`
	ID     string `json:"id"`
	Bundle struct {
		FunctionID      string          `json:"function_id"`
		Profile         string          `json:"profile"`
		CallerVariables []string        `json:"caller_variables"`
		Limits          h.Limits        `json:"limits"`
		Environment     json.RawMessage `json:"environment"`
		Source          string          `json:"source"`
		Fixed           json.RawMessage `json:"fixed"`
	} `json:"bundle"`
	Pin   map[string]any `json:"pin"`
	Loads []struct {
		Name    string         `json:"name"`
		Replace map[string]any `json:"pin_replace"`
		Expect  vectorDiag     `json:"expect"`
	} `json:"loads"`
	Steps []struct {
		Name   string `json:"name"`
		Cursor int64  `json:"cursor"`
		Date   string `json:"date"`
		Inputs string `json:"inputs_json"`
		Expect struct {
			Entry *struct {
				Cursor      int64  `json:"cursor"`
				SnapshotRef string `json:"snapshot_ref"`
				Date        string `json:"date"`
				InputsRef   string `json:"inputs_ref"`
				StaleReason string `json:"stale_reason"`
				Response    struct {
					Status     string      `json:"status"`
					Value      *bool       `json:"value"`
					Diagnostic *vectorDiag `json:"diagnostic"`
				} `json:"response"`
			} `json:"entry"`
			Diagnostic *vectorDiag `json:"diagnostic"`
		} `json:"expect"`
	} `json:"steps"`
}

// files gives the bundle of a vector, bound to the build that runs the test.
func (v *vector) files(t *testing.T) h.Files {
	env, err := json.Marshal(v.Bundle.Environment)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := json.Marshal(v.Bundle.Fixed)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"function_id": v.Bundle.FunctionID, "profile": v.Bundle.Profile, "caller_variables": v.Bundle.CallerVariables,
		"implementation_ref": h.BuildID(), "limits": v.Bundle.Limits,
		"environment_ref": h.Digest("application/json", env), "source_ref": h.Digest("text/plain", []byte(v.Bundle.Source)),
		"fixed_ref": h.Digest("application/json", fixed)})
	if err != nil {
		t.Fatal(err)
	}
	return h.Files{Manifest: manifest, Environment: env, Source: []byte(v.Bundle.Source), Fixed: fixed}
}

func sameDiag(d *h.Diagnostic, want vectorDiag) bool {
	return d != nil && d.Code == want.Code && d.Stage == want.Stage && d.Field == want.Field
}

func TestExecutorVectors(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(vectorDir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no executor vectors: %v", err)
	}
	type result struct {
		Name       string        `json:"name"`
		Entry      *h.Entry      `json:"entry,omitempty"`
		Diagnostic *h.Diagnostic `json:"diagnostic,omitempty"`
	}
	all := map[string][]result{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(raw, &v); err != nil || v.Format != "executor-vector/v1" {
			t.Fatalf("%s: bad vector: %v", path, err)
		}
		files := v.files(t)
		bundle, d := h.Load(files)
		if d != nil {
			t.Fatalf("%s: bundle: %v", v.ID, d)
		}
		pin := func(replace map[string]any) []byte {
			p := map[string]any{"bundle_ref": bundle.Describe().BundleRef, "engine_build": h.BuildID()}
			for k, x := range v.Pin {
				p[k] = x
			}
			for k, x := range replace {
				p[k] = x
			}
			out, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		var results []result
		for _, load := range v.Loads {
			_, d := h.NewExecutor(pin(load.Replace), files)
			if !sameDiag(d, load.Expect) {
				t.Errorf("%s/%s: load gave %+v, want %+v", v.ID, load.Name, d, load.Expect)
			}
			results = append(results, result{Name: load.Name, Diagnostic: d})
		}
		e, d := h.NewExecutor(pin(nil), files)
		if d != nil {
			t.Fatalf("%s: load: %v", v.ID, d)
		}
		for _, step := range v.Steps {
			entry, d := e.Invoke(context.Background(), step.Cursor, step.Date, []byte(step.Inputs))
			results = append(results, result{Name: step.Name, Entry: entry, Diagnostic: d})
			name := v.ID + "/" + step.Name
			if want := step.Expect.Diagnostic; want != nil {
				if entry != nil || !sameDiag(d, *want) {
					t.Errorf("%s: gave %+v %+v, want refusal %+v", name, entry, d, *want)
				}
				continue
			}
			want := step.Expect.Entry
			if d != nil || entry == nil {
				t.Errorf("%s: refused: %v", name, d)
				continue
			}
			got := entry.Response
			if entry.Cursor != want.Cursor || entry.SnapshotRef != want.SnapshotRef || entry.Date != want.Date ||
				entry.InputsRef != want.InputsRef || entry.StaleReason != want.StaleReason ||
				got.Status != want.Response.Status || !reflect.DeepEqual(got.Value, want.Response.Value) ||
				got.BundleRef != bundle.Describe().BundleRef || got.ImplementationRef != h.BuildID() {
				t.Errorf("%s: entry %+v response %+v, want %+v", name, entry, got, want)
			}
			if (got.Status == "ok") != (got.Cost != nil) {
				t.Errorf("%s: cost %v for status %s", name, got.Cost, got.Status)
			}
			if wd := want.Response.Diagnostic; (wd == nil) != (got.Diagnostic == nil) || wd != nil && !sameDiag(got.Diagnostic, *wd) {
				t.Errorf("%s: response diagnostic %+v, want %+v", name, got.Diagnostic, wd)
			}
		}
		all[v.ID] = results
	}
	if out := os.Getenv("EXECUTOR_VECTOR_RESULTS"); out != "" {
		raw, err := json.MarshalIndent(map[string]any{"implementation_ref": h.BuildID(), "vectors": all}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(raw, '\n'), 0o666); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEntryJSONHasTheIngestFields(t *testing.T) {
	value := true
	raw, err := h.MarshalEntry(&h.Entry{Cursor: 3, SnapshotRef: "s", Date: "2026-11-15", InputsRef: "i",
		Response: h.Response{Status: "ok", Value: &value, BundleRef: "<b>"}, StaleReason: ""})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cursor":3,"snapshot_ref":"s","date":"2026-11-15","inputs_ref":"i","response":{"status":"ok","value":true,"bundle_ref":"<b>"},"stale_reason":""}`
	if string(raw) != want {
		t.Fatalf("got %s", raw)
	}
}
