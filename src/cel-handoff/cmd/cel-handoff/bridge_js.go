//go:build js && wasm

package main

// The bridge gives the executor to JavaScript as globalThis.entelosExecutor.
// Each function returns one JSON string: {"ok": value} or {"diagnostic": d}.
// Under Node with arguments, the same module serves the command line, so the
// lifecycle Host can qualify a bundle with the exact module that a browser
// loads.

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"syscall/js"

	h "example.org/cel-handoff"
)

// bridgeMode is true when the module has no command arguments. The Go
// JavaScript support gives a browser module the single argument "js".
func bridgeMode() bool { return len(os.Args) <= 1 }

func reply(value any, d *h.Diagnostic) any {
	out := map[string]any{"ok": value}
	if d != nil {
		out = map[string]any{"diagnostic": d}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"diagnostic": &h.Diagnostic{Code: "internal_error", Stage: "bridge", Message: err.Error()}})
	}
	return string(raw)
}

func refuse(code, field, message string) any {
	return reply(nil, &h.Diagnostic{Code: code, Stage: "bridge", Field: field, Message: message})
}

// bytesArg copies a Uint8Array argument. Bundle files are bytes, so the
// bridge does not take them as JavaScript strings.
func bytesArg(v js.Value) ([]byte, bool) {
	if v.Type() != js.TypeObject || !v.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, false
	}
	out := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(out, v)
	return out, true
}

func serveBridge() {
	var executor *h.Executor
	api := js.Global().Get("Object").New()
	api.Set("build", js.FuncOf(func(js.Value, []js.Value) any {
		return reply(h.BuildID(), nil)
	}))
	// load(pinJSON, {manifest, environment, source, fixed}) replaces the
	// loaded executor only if the new pin and bundle load.
	api.Set("load", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeObject {
			return refuse("invalid_request", "", "load takes a pin JSON string and a files object")
		}
		var f h.Files
		// A fixed order gives one diagnostic for one call.
		for _, file := range []struct {
			name string
			dst  *[]byte
		}{{"manifest", &f.Manifest}, {"environment", &f.Environment}, {"source", &f.Source}, {"fixed", &f.Fixed}} {
			raw, ok := bytesArg(args[1].Get(file.name))
			if !ok {
				return refuse("invalid_request", "/"+file.name, "each bundle file must be a Uint8Array")
			}
			*file.dst = raw
		}
		next, d := h.NewExecutor([]byte(args[0].String()), f)
		if d != nil {
			return reply(nil, d)
		}
		executor = next
		return reply(executor.Describe(), nil)
	}))
	// invoke(cursor, date, inputsJSON) gives one log entry. The caller
	// stores the entry at its cursor and keeps the raw inputs.
	api.Set("invoke", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if executor == nil {
			return refuse("revision_not_loaded", "", "load a pinned activation first")
		}
		if len(args) != 3 || args[0].Type() != js.TypeNumber || args[1].Type() != js.TypeString || args[2].Type() != js.TypeString {
			return refuse("invalid_request", "", "invoke takes a cursor number, a date string and an inputs JSON string")
		}
		// The executor checks the cursor range. The bridge refuses only a
		// number that is not an integer in the int64 range, with the same
		// stage as the executor.
		n := args[0].Float()
		if n != math.Trunc(n) || math.Abs(n) >= 1<<62 {
			return reply(nil, &h.Diagnostic{Code: "invalid_request", Stage: "log", Field: "/cursor", Message: "cursor must be an integer from 1 to 2^53-1"})
		}
		entry, d := executor.Invoke(context.Background(), int64(n), args[1].String(), []byte(args[2].String()))
		if d != nil {
			return reply(nil, d)
		}
		return reply(entry, nil)
	}))
	js.Global().Set("entelosExecutor", api)
	if ready := js.Global().Get("entelosExecutorReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}
