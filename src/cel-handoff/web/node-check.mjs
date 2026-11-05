// node node-check.mjs MODULE.wasm WASM_EXEC.js VECTOR_DIR RESULTS.json
// Loads the module as a browser does (no arguments), runs the executor
// vectors through the bridge and writes the results. Exit 1 on an error.
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { runVectors } from "./executor-vectors.mjs";

const [modulePath, wasmExec, vectorDir, out] = process.argv.slice(2);
await import(pathToFileURL(wasmExec).href);
const ready = new Promise((resolve) => { globalThis.entelosExecutorReady = resolve; });
const go = new globalThis.Go();
const started = performance.now();
const { instance } = await WebAssembly.instantiate(readFileSync(modulePath), go.importObject);
go.run(instance);
await ready;
const coldStartMs = performance.now() - started;
const vectors = readdirSync(vectorDir).filter((n) => n.endsWith(".json")).sort()
  .map((n) => JSON.parse(readFileSync(join(vectorDir, n), "utf8")));
const result = await runVectors(globalThis.entelosExecutor, vectors);
result.host = { runtime: "node", version: process.version, cold_start_ms: Math.round(coldStartMs) };
writeFileSync(out, JSON.stringify(result, null, 1) + "\n");
console.log(JSON.stringify({ errors: result.errors.length, vectors: Object.keys(result.vectors).length, cold_start_ms: result.host.cold_start_ms }));
process.exit(result.errors.length ? 1 : 0);
