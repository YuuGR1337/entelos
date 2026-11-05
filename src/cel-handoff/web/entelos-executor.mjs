// The browser side of the executor: load the WebAssembly module, keep a
// local receipt log in IndexedDB and export the log for the lifecycle
// ingest operation. Raw inputs stay in the browser unless the application
// asks for them in the export.

const encoder = new TextEncoder();

// ExecutorError carries the structured diagnostic of a refusal.
export class ExecutorError extends Error {
  constructor(diagnostic) {
    super(`${diagnostic.stage}: ${diagnostic.code}: ${diagnostic.message}`);
    this.diagnostic = diagnostic;
  }
}

// contentRef is the content identity of bytes with a media type, as the
// lifecycle records use it. It is not a signature.
export async function contentRef(media, bytes) {
  const head = encoder.encode(media + "\0");
  const data = new Uint8Array(head.length + bytes.length);
  data.set(head);
  data.set(bytes, head.length);
  const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", data));
  return "sha256:" + Array.from(sum, (b) => b.toString(16).padStart(2, "0")).join("");
}

function result(raw) {
  const r = JSON.parse(raw);
  if (r.diagnostic) throw new ExecutorError(r.diagnostic);
  return r.ok;
}

// loadModule starts the module and gives an executor. The module bytes must
// have the content reference expectedModuleRef (media application/wasm), so
// a changed module fails closed before it runs. wasmExecUrl names the Go
// support script wasm_exec.js of the same Go release that built the module.
// A page can run one module. The function copies the bytes first and checks
// and runs that copy, so a later change to the caller's buffer has no effect.
export async function loadModule({ moduleBytes, expectedModuleRef, wasmExecUrl }) {
  const bytes = new Uint8Array(moduleBytes).slice();
  const ref = await contentRef("application/wasm", bytes);
  if (ref !== expectedModuleRef) {
    throw new ExecutorError({ code: "artifact_mismatch", stage: "module", field: "/module", message: `module ${ref} is not ${expectedModuleRef}` });
  }
  if (globalThis.entelosExecutor) {
    throw new ExecutorError({ code: "invalid_request", stage: "module", message: "this page already runs a module" });
  }
  if (typeof globalThis.Go !== "function") await import(wasmExecUrl);
  const ready = new Promise((resolve) => { globalThis.entelosExecutorReady = resolve; });
  const go = new globalThis.Go();
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
  go.run(instance);
  await ready;
  delete globalThis.entelosExecutorReady;
  return new Executor(globalThis.entelosExecutor, ref);
}

export class Executor {
  constructor(api, moduleRef) {
    this.api = api;
    this.moduleRef = moduleRef;
    this.pending = 0;
  }

  // build gives the implementation_ref of the module.
  build() { return result(this.api.build()); }

  // load checks the pin against the bundle files (Uint8Array values) and
  // gives the callable descriptor. The bridge evaluates an invocation when its
  // log transaction runs. Thus load refuses while an invocation is pending, so
  // a new pin never answers an invocation that the caller made under the old pin.
  load(pin, files) {
    if (this.pending > 0) {
      throw new ExecutorError({ code: "invalid_request", stage: "load", message: `${this.pending} invocations are pending; load after they complete` });
    }
    return result(this.api.load(JSON.stringify(pin), files));
  }

  // invoke evaluates inputs at date and appends the entry to the log at the
  // next cursor, in one IndexedDB transaction. A refusal throws an
  // ExecutorError and writes nothing.
  invoke(log, date, inputs) {
    const inputsJSON = JSON.stringify(inputs);
    this.pending++;
    return log.append((cursor) => JSON.parse(this.api.invoke(cursor, date, inputsJSON)), inputsJSON)
      .finally(() => { this.pending--; });
  }
}

function request(r) {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

// ReceiptLog is the local log of one executor in one IndexedDB database. The
// cursor is the key, from 1. The raw inputs are in a separate store.
export class ReceiptLog {
  static async open(name) {
    const r = indexedDB.open(name, 1);
    r.onupgradeneeded = () => {
      r.result.createObjectStore("entries", { keyPath: "cursor" });
      r.result.createObjectStore("inputs", { keyPath: "cursor" });
    };
    return new ReceiptLog(await request(r));
  }

  constructor(db) { this.db = db; }

  close() { this.db.close(); }

  // append runs write(nextCursor) in the transaction that stores its entry,
  // so two appends never take the same cursor. write is synchronous and
  // gives {ok: entry} or {diagnostic}.
  append(write, inputsJSON) {
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction(["entries", "inputs"], "readwrite");
      const entries = tx.objectStore("entries");
      let entry, refusal;
      const last = entries.openCursor(null, "prev");
      last.onsuccess = () => {
        const cursor = last.result ? last.result.key + 1 : 1;
        let r;
        try {
          r = write(cursor);
        } catch (error) {
          refusal = error;
          tx.abort();
          return;
        }
        if (r.diagnostic) {
          refusal = new ExecutorError(r.diagnostic);
          tx.abort();
          return;
        }
        entry = r.ok;
        if (entry.cursor !== cursor) {
          refusal = new ExecutorError({ code: "internal_error", stage: "log", message: `entry cursor ${entry.cursor} is not ${cursor}` });
          tx.abort();
          return;
        }
        entries.add(entry);
        tx.objectStore("inputs").add({ cursor, inputs_json: inputsJSON });
      };
      tx.oncomplete = () => resolve(entry);
      tx.onabort = () => reject(refusal ?? tx.error);
    });
  }

  // entries gives the log from cursor `from`, in cursor order. If
  // includeInputs is true, each entry has its raw inputs, for an ingest
  // with an upload policy that permits them.
  async entries({ from = 1, includeInputs = false } = {}) {
    const tx = this.db.transaction(["entries", "inputs"], "readonly");
    const range = IDBKeyRange.lowerBound(from);
    const list = await request(tx.objectStore("entries").getAll(range));
    if (!includeInputs) return list;
    const inputs = await request(tx.objectStore("inputs").getAll(range));
    const byCursor = new Map(inputs.map((x) => [x.cursor, JSON.parse(x.inputs_json)]));
    return list.map((e) => ({ ...e, inputs: byCursor.get(e.cursor) }));
  }
}
