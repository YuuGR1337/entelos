# 0001 Technology for the core, sidecar and protocol

- Status: accepted
- Date: 2026-11-05
- Decided by: the maintainer
- Issue: [#85](https://github.com/mohanraj00/entelos/issues/85), part of [M6](https://github.com/mohanraj00/entelos/issues/77)

## Question

M6 needs a store-neutral lifecycle core, a versioned wire protocol and a local sidecar. Which language, encoding and edge binding do they use?

## Decision

1. **Core, sidecar and server units: Go.**
2. **Protocol: JSON messages with a JSON Schema (2020-12), length-prefixed on a Unix domain socket. Record digests are SHA-256 over the RFC 8785 (JCS) canonical form.**
3. **Edge binding: a gomobile package that contains only the executor, for native Swift and Kotlin apps.** The maintainer chose native apps as the edge target. The choice between an embedded executor and a sidecar on other edge devices is still open.

## Reasons

### Language

| | Go | Rust | Python (core in v0.1.0) |
|---|---|---|---|
| CEL | cel-go, the reference implementation, already pinned here | Community crate; conformance not complete | Weak maintenance |
| Sidecar as one static binary | Yes | Yes | No |
| Kubernetes server units | Native ecosystem | Good | Fair |
| Phones | gomobile; works, binary is large | uniffi; strong | No |
| Cost to port the v0.1.0 core (about 900 lines) | Low; the CEL host is already Go | Medium | None |

An executor evaluates the same rule that qualification passed. One CEL implementation on servers and on edge devices keeps the qualified build and the executed build the same. A second CEL implementation would be a different build identity.

### Encoding

| | JSON + JSON Schema | Protobuf | CBOR |
|---|---|---|---|
| Readable vectors in the repo | Yes | Only through the JSON mapping | No |
| Deterministic bytes for digests | RFC 8785 | Not canonical | RFC 8949 deterministic profile |
| Generated typed clients | Weak | Strong | Weak |
| Same as the v0.1.0 records | Yes | No | No |

The trade-off: there are no generated typed clients. A thin client for each language is written by hand. RFC 8785 is also a common canonical form for content digests in other stores, so an adapter does not need a second canonical form.

### Edge binding

| Option | Fit |
|---|---|
| gomobile package (Swift, Kotlin) | Same cel-go as the servers. Binary size is unmeasured. |
| Go `c-shared` library and FFI | Works on Android. More work on iOS. |
| WASM module | Needs a runtime in the app. CEL speed there is unmeasured. |
| Sidecar process | Not possible on iOS. |
| Pure JavaScript executor | Good for web and React Native apps. It is a second CEL implementation, so each rule needs qualification on both engines, and the receipt must record the engine. |
| Go executor compiled to WASM, called from JavaScript | One CEL implementation. The binary is several MB (unmeasured). The React Native Hermes engine does not run WASM by default. |

The edge apps are native Swift and Kotlin apps, so gomobile is the best fit. If a web or React Native app becomes a target, examine the two JavaScript options again.

## Consequences

- The lifecycle core moves from Python to Go. The Python core stays as the reference until the Go core passes the same conformance vectors. A separate change removes it.
- Storage and protocol vectors are JSON, so the same files test both cores.
- Windows stays outside the tested scope, because the socket and the concurrency test are POSIX.

## Limits

- No benchmark was run. The binary size and speed claims for gomobile and WASM are unmeasured.
- If the embedded executor is chosen, measure the gomobile binary size on one phone build before more work. That measurement answers a named open question.
