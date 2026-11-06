// The page that browser_check.py loads in a headless browser. It reads
// job.json, loads the module, runs the executor vectors through the bridge
// and, if the job has a session, invokes through the IndexedDB log. It posts
// the observations to /result. browser_check.py and its caller judge them.
import { loadModule, ReceiptLog } from "./entelos-executor.mjs";
import { runVectors } from "./executor-vectors.mjs";

const encoder = new TextEncoder();
const out = { errors: [] };

async function attempt(fn) {
  try {
    return { ok: await fn() };
  } catch (e) {
    return e.diagnostic ? { diagnostic: e.diagnostic } : { error: String(e && e.stack || e) };
  }
}

async function session(executor, s) {
  const files = Object.fromEntries(Object.entries(s.files).map(([k, v]) => [k, encoder.encode(v)]));
  const loaded = await attempt(() => executor.load(s.pin, files));
  if (!loaded.ok) return { loaded };
  let log = await ReceiptLog.open(s.log_name);
  const invokes = [];
  for (const step of s.invokes) {
    invokes.push({ name: step.name, ...(await attempt(() => executor.invoke(log, step.date, step.inputs))) });
  }
  // Two invokes at the same time must take two cursors.
  const concurrent = await Promise.all(s.concurrent.map((step) =>
    attempt(() => executor.invoke(log, step.date, step.inputs)).then((r) => ({ name: step.name, ...r }))));
  // A load while an invocation is pending must fail, and the pending
  // invocation must answer under the pin that was loaded when it was made.
  const pending = attempt(() => executor.invoke(log, s.pending.date, s.pending.inputs)).then((r) => ({ name: s.pending.name, ...r }));
  const pendingLoad = await attempt(() => executor.load(s.pin, files));
  const pendingInvoke = await pending;
  const before = await log.entries();
  log.close();
  log = await ReceiptLog.open(s.log_name);
  const after = await log.entries();
  const exported = await log.entries({ includeInputs: s.include_inputs });
  log.close();
  return { loaded, invokes, concurrent, pending_load: pendingLoad, pending: pendingInvoke, reopened_equal: JSON.stringify(before) === JSON.stringify(after), entries: exported };
}

try {
  const job = await (await fetch("job.json")).json();
  const t0 = performance.now();
  const bytes = new Uint8Array(await (await fetch("module.wasm")).arrayBuffer());
  const t1 = performance.now();
  // A changed byte must fail closed before the module runs.
  const tampered = bytes.slice();
  tampered[tampered.length - 1] ^= 1;
  out.tampered = await attempt(() => loadModule({ moduleBytes: tampered, expectedModuleRef: job.module_ref, wasmExecUrl: "./wasm_exec.js" }));
  const t2 = performance.now();
  // The caller changes its buffer after the call. The module must still start,
  // because loadModule checks and runs its own copy. The changed byte is in
  // the WebAssembly magic number, so a run of the changed bytes cannot start.
  const callerBytes = bytes.slice();
  const started = loadModule({ moduleBytes: callerBytes.buffer, expectedModuleRef: job.module_ref, wasmExecUrl: "./wasm_exec.js" });
  callerBytes[0] ^= 1;
  // A second call during the start must fail and must not take the start of the first call.
  out.concurrent_start = await attempt(() => loadModule({ moduleBytes: bytes, expectedModuleRef: job.module_ref, wasmExecUrl: "./wasm_exec.js" }));
  const executor = await started;
  const t3 = performance.now();
  out.changed_after_call = { started: true };
  out.timing = { fetch_ms: Math.round(t1 - t0), start_ms: Math.round(t3 - t2) };
  out.user_agent = navigator.userAgent;
  out.module_ref = executor.moduleRef;
  out.implementation_ref = executor.build();
  out.vectors = await runVectors(executor.api, job.vectors);
  if (job.session) out.session = await session(executor, job.session);
} catch (e) {
  out.errors.push(String(e && e.stack || e));
}
await fetch("result", { method: "POST", body: JSON.stringify(out) });
