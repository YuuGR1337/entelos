// Runs the executor vectors through the JavaScript bridge of the WebAssembly
// module. It does the same checks as executor_test.go and gives results in
// the same shape, so that a check can compare the bridge with a native build.
// It uses only Web APIs, so it runs in Node and in a browser.

const encoder = new TextEncoder();

async function digest(media, body) {
  const data = new Uint8Array([...encoder.encode(media + "\0"), ...body]);
  const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", data));
  return "sha256:" + Array.from(sum, (b) => b.toString(16).padStart(2, "0")).join("");
}

function call(fn, ...args) {
  return JSON.parse(fn(...args));
}

function sameDiag(d, want) {
  return d !== undefined && d.code === want.code && d.stage === want.stage && (d.field ?? "") === want.field;
}

// files gives the bundle of a vector, bound to the build of the module.
async function files(vector, build) {
  const b = vector.bundle;
  const environment = encoder.encode(JSON.stringify(b.environment));
  const source = encoder.encode(b.source);
  const fixed = encoder.encode(JSON.stringify(b.fixed));
  const manifest = {
    caller_variables: b.caller_variables, environment_ref: await digest("application/json", environment),
    fixed_ref: await digest("application/json", fixed), function_id: b.function_id, implementation_ref: build,
    limits: b.limits, profile: b.profile, source_ref: await digest("text/plain", source),
  };
  return { manifest: encoder.encode(JSON.stringify(manifest)), environment, source, fixed };
}

// runVectors gives {implementation_ref, vectors, errors}. An empty errors
// list means that each step gave its expected result.
export async function runVectors(api, vectors) {
  const errors = [];
  const build = call(api.build).ok;
  const all = {};
  for (const v of vectors) {
    if (v.format !== "executor-vector/v1") {
      errors.push(`${v.id}: unknown format ${v.format}`);
      continue;
    }
    const bundle = await files(v, build);
    const bundleRef = await digest("application/vnd.cel-handoff.bundle+json", bundle.manifest);
    const pin = (replace) => JSON.stringify({ bundle_ref: bundleRef, engine_build: build, ...v.pin, ...replace });
    const results = [];
    for (const load of v.loads ?? []) {
      const r = call(api.load, pin(load.pin_replace), bundle);
      if (!sameDiag(r.diagnostic, load.expect)) {
        errors.push(`${v.id}/${load.name}: load gave ${JSON.stringify(r)}`);
      }
      results.push({ name: load.name, diagnostic: r.diagnostic });
    }
    const loaded = call(api.load, pin({}), bundle);
    if (loaded.diagnostic || loaded.ok.bundle_ref !== bundleRef) {
      errors.push(`${v.id}: load gave ${JSON.stringify(loaded)}`);
      continue;
    }
    for (const step of v.steps) {
      const r = call(api.invoke, step.cursor, step.date, step.inputs_json);
      const name = `${v.id}/${step.name}`;
      results.push({ name: step.name, entry: r.ok, diagnostic: r.diagnostic });
      const wantDiag = step.expect.diagnostic;
      if (wantDiag) {
        if (r.ok || !sameDiag(r.diagnostic, wantDiag)) errors.push(`${name}: gave ${JSON.stringify(r)}`);
        continue;
      }
      const want = step.expect.entry;
      const e = r.ok;
      if (!e) {
        errors.push(`${name}: refused ${JSON.stringify(r.diagnostic)}`);
        continue;
      }
      const got = e.response;
      const fields = ["cursor", "snapshot_ref", "date", "inputs_ref", "stale_reason"];
      if (fields.some((k) => e[k] !== want[k]) || got.status !== want.response.status || got.value !== want.response.value ||
          got.bundle_ref !== bundleRef || got.implementation_ref !== build || (got.status === "ok") !== (got.cost !== undefined)) {
        errors.push(`${name}: entry ${JSON.stringify(e)}`);
      }
      const wd = want.response.diagnostic;
      if ((wd === undefined) !== (got.diagnostic === undefined) || (wd && !sameDiag(got.diagnostic, wd))) {
        errors.push(`${name}: response diagnostic ${JSON.stringify(got.diagnostic)}`);
      }
    }
    all[v.id] = results;
  }
  return { implementation_ref: build, vectors: all, errors };
}
