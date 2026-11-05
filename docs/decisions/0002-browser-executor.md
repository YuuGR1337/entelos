# 0002 Browser executor for web delivery

- Status: accepted
- Date: 2026-11-05
- Decided by: the maintainer
- Amends: [0001](0001-technology.md), item 3 (edge binding)
- Amended: 2026-11-05, qualification for each build (see [Amendment](#amendment-qualification-for-each-build))
- Issue: [#93](https://github.com/mohanraj00/entelos/issues/93), part of [M6](https://github.com/mohanraj00/entelos/issues/77)

## Question

Decision 0001 gives a gomobile executor for native Swift and Kotlin apps. Some users on tablets and phones do not want to install an app. They use a web application in a browser. How does an executor run in that delivery mode?

## Decision

1. **Entelos supports each delivery mode: servers, native apps, and web applications in a browser.**
2. **The browser executor is the same Go executor, compiled to WebAssembly (WASM).** A thin JavaScript bridge loads the module and calls it. The gomobile package for native apps stays as decision 0001 says.
3. **There is no second CEL implementation.** A pure JavaScript CEL engine is not used.

## Reasons

| | Go compiled to WASM | Pure JavaScript CEL engine |
|---|---|---|
| CEL implementation | cel-go, the same as the servers and gomobile | A second implementation |
| Qualification | One qualification for each rule and build, on one CEL implementation | Each rule needs qualification on both engines |
| Receipt | Records the build identity, as on other executors | Must also record the engine |
| Size | Several MB (unmeasured) | Smaller |
| Runtime | All current browsers run WASM | All JavaScript engines |

An executor must evaluate the same rule that qualification passed. One CEL implementation on each delivery mode keeps the qualified build and the executed build the same. The cost is a larger download, and this is measured in #93.

## Consequences

- The executor code must build for `GOOS=js GOARCH=wasm`. It must not use the file system, processes or sockets directly. The host (gomobile, sidecar or JavaScript bridge) supplies storage and transport.
- The browser keeps the local receipt log in browser storage (IndexedDB or OPFS). Ingest stays idempotent on (executor ID, cursor).
- The browser executor fails closed on expiry or revocation, the same as the other executors.
- Raw inputs stay in the browser unless the application permits an upload.
- CI must run the executor vectors in a headless browser.

## Limits

- The module size and the cold-start time are unmeasured. #93 measures them.
- React Native with the Hermes engine does not run WASM by default. This decision does not cover it.
- The choice between an embedded executor and a sidecar on other edge devices is still open.

## Amendment: qualification for each build

- Date: 2026-11-05
- Decided by: the maintainer, in the #93 work

A bundle manifest pins `implementation_ref`. The build identity includes `GOOS` and `GOARCH`, so the WASM module has a different identity from a native build. A bundle that qualification passed on a native build fails closed in the browser with `implementation_unavailable`.

The maintainer chose to keep the identity strict. Each build has its own qualification:

1. The WASM module keeps its own `implementation_ref`. A bundle for web delivery binds that identity.
2. The same module serves the command line under Node. Thus the Host qualifies, activates and replays with the exact module that a browser loads.
3. The executor vectors give the same entries on each build. Only the build identity and the bundle reference differ.

The other options were a platform-neutral identity and a snapshot that pins a set of builds. A platform-neutral identity puts one qualification on different compiled bytes. A set of builds changes the propose, qualify, snapshot and receipt contracts. A later issue can propose the set if one rule must serve native and web callers from one approval.
