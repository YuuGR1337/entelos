// cel-handoff loads a host-selected bundle and serves a local JSONL caller.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"

	h "example.org/cel-handoff"
)

func emit(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func fail(d *h.Diagnostic) {
	emit(h.Response{Status: d.Code, ImplementationRef: h.BuildID(), Diagnostic: d})
	os.Exit(1)
}
func readBound(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if len(raw) > 65536 {
		return nil, fmt.Errorf("file exceeds metadata ceiling")
	}
	return raw, err
}
func main() {
	// A WebAssembly module in a browser has no arguments. It serves the
	// JavaScript bridge instead of the command line. See bridge_js.go.
	if bridgeMode() {
		serveBridge()
		return
	}
	dir := flag.String("bundle", "", "host-selected directory with manifest.json, env.json, rule.cel, fixed.json")
	casesPath := flag.String("cases", "", "host-owned check cases JSON array")
	build := flag.Bool("build", false, "print actual implementation identity and dependency build data")
	flag.Parse()
	if *build {
		info, _ := debug.ReadBuildInfo()
		emit(map[string]any{"implementation_ref": h.BuildID(), "build": info})
		return
	}
	if *dir == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: cel-handoff -bundle DIR [-cases FILE] describe|check|run; or -build")
		os.Exit(2)
	}
	var f h.Files
	for name, dst := range map[string]*[]byte{"manifest.json": &f.Manifest, "env.json": &f.Environment, "rule.cel": &f.Source, "fixed.json": &f.Fixed} {
		raw, err := readBound(filepath.Join(*dir, name))
		if err != nil {
			fail(&h.Diagnostic{Code: "artifact_unavailable", Stage: "bundle", Field: "/" + name, Message: err.Error()})
		}
		*dst = raw
	}
	b, d := h.Load(f)
	if d != nil {
		fail(d)
	}
	switch flag.Arg(0) {
	case "describe":
		emit(b.Describe())
	case "check":
		if *casesPath == "" {
			fail(&h.Diagnostic{Code: "invalid_cases", Stage: "check", Message: "-cases is required; no activation is implied"})
		}
		raw, err := readBound(*casesPath)
		if err != nil {
			fail(&h.Diagnostic{Code: "invalid_cases", Stage: "check", Message: err.Error()})
		}
		// Check data is host-owned. It is not an invocation field or serving authority.
		var cases []h.Case
		if err := json.Unmarshal(raw, &cases); err != nil {
			fail(&h.Diagnostic{Code: "invalid_cases", Stage: "check", Message: err.Error()})
		}
		report := b.Check(context.Background(), cases)
		emit(report)
		if report.Status != "passed" {
			os.Exit(1)
		}
	case "run":
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 512), b.Describe().Limits.MaxRequestBytes+1)
		for scanner.Scan() {
			emit(b.Run(context.Background(), scanner.Bytes()))
		}
		if err := scanner.Err(); err != nil {
			fail(&h.Diagnostic{Code: "invalid_input", Stage: "input", Message: err.Error()})
		}
	default:
		fail(&h.Diagnostic{Code: "invalid_operation", Stage: "request", Message: "expected describe, check or run"})
	}
}
