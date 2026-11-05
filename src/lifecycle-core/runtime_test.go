package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// fakeRuntime is a CEL runtime whose executable is a shell script.
func fakeRuntime(t *testing.T, script string) *CELRuntime {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "cel")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt, err := NewCELRuntime(binary, filepath.Join(dir, "work"))
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

var emptyFiles = map[string][]byte{"manifest": {}, "environment": {}, "source": {}, "fixed": {}}

func TestRuntimeFaultHoldsStderr(t *testing.T) {
	rt := fakeRuntime(t, `echo "panic: engine stopped" >&2; exit 3`)
	_, err := rt.Execute(emptyFiles, "run", nil, nil)
	f, ok := err.(*Fault)
	if !ok || f.Code != "implementation_unavailable" || !strings.Contains(f.Message, "stderr: panic: engine stopped") {
		t.Fatalf("got %v", err)
	}
	long := fakeRuntime(t, `head -c 100000 /dev/zero | tr '\0' x >&2; exit 3`)
	_, err = long.Execute(emptyFiles, "run", nil, nil)
	if f, ok := err.(*Fault); !ok || len(f.Message) > maxStderr+200 || !strings.HasSuffix(f.Message, "[truncated]") {
		t.Fatalf("stderr is not bounded: %d bytes", len(err.Error()))
	}
}

// A check report with exit 1 is a failed qualification, not a runtime fault.
// A describe with a nonzero exit is a fault. This is the cel_runtime.py rule.
func TestRuntimeExitRulesFollowTheReference(t *testing.T) {
	rt := fakeRuntime(t, `echo '{"status":"failed","diagnostic":{"message":"case 1"}}'; exit 1`)
	value, err := rt.Execute(emptyFiles, "check", nil, Array{})
	if err != nil || lookupAny(value, "status") != "failed" {
		t.Fatalf("check: %v %v", value, err)
	}
	if _, err := rt.Execute(emptyFiles, "describe", nil, nil); code(err) != "failed" || err.(*Fault).Message != "case 1" {
		t.Fatalf("describe: %v", err)
	}
}

func TestFloatTextErrorsAreCodecFaults(t *testing.T) {
	for _, text := range []string{"1x", "", "1e", "--1"} {
		if v, err := parseFloat(text); code(err) != "invalid_record" {
			t.Errorf("%q: got %v %v", text, v, err)
		}
	}
	if v, err := parseFloat("1e400"); code(err) != "invalid_record" {
		t.Errorf("overflow: got %v %v", v, err)
	}
	if v, err := parseFloat("1e-400"); err != nil || v != 0.0 {
		t.Errorf("underflow must give 0.0 as in Python: got %v %v", v, err)
	}
}

// The build identity holds only the core sources, so a test change does not
// change it. Compute the expected identity from the files on disk.
func TestBuildIdentityLeavesOutTests(t *testing.T) {
	names, _ := filepath.Glob("*.go")
	sort.Strings(names)
	var body []byte
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body = append(append(append(append(body, name...), 0), raw...), 0)
	}
	git, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	body = append(append(body, runtime.Version()...), git...)
	got, err := Implementation()
	if err != nil || got != ContentRef(Envelope(Media("implementation"), body)) {
		t.Fatalf("identity %s %v differs from the core sources", got, err)
	}
}
