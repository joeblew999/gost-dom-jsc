<!-- 2026-10-01-01-gost-dom-jsc-purego-issue.md -->
<!-- Title: Proposal: cgo-free JavaScriptCore engine via purego (spike results + implementation checklist) -->
<!-- Post: gh issue create -R gost-dom/browser --title "Proposal: cgo-free JavaScriptCore engine via purego (spike results + implementation checklist)" --body-file docs/2026-10-01-01-gost-dom-jsc-purego-issue.md -->

## Summary

Spike code: https://github.com/joeblew999/gost-dom-jsc/tree/main/spike

Proposal to add a third script engine, **JavaScriptCore (JSC) loaded via [purego](https://github.com/ebitengine/purego)**, alongside `v8engine` and `sobekengine`. JSC has a plain C API, so it can be called from Go **with `CGO_ENABLED=0`**. It also has a JIT, giving V8-class JS speed in a pure-Go build.

A working spike (details below) confirms that the Web-IDL patterns Gost-DOM relies on all work through purego with **7 shared callbacks total**, regardless of how many classes are defined. This issue lists what was proven, what still needs checking, and the exact touch points in this repo, so the work can be picked up by a person or an agent.

## Why

| | v8engine | sobekengine | **JSC via purego** |
|---|---|---|---|
| cgo / C++ toolchain | required | no | **no** |
| Modern JS + JIT | yes | no JIT | **yes** |
| `go build` incremental (spike) | ~2.1 s | fast | **~0.27 s** |
| Test binary size (spike) | ~58 MB | small | **~2.6 MB** (+ system lib) |
| Platforms | Linux, macOS | all | Linux, macOS (not Windows) |

## Spike results

Environment: Ubuntu 24.04, `libjavascriptcoregtk-6.0` 2.52.6, Go 1.25.1, purego v0.11.1, `CGO_ENABLED=0`, 1 vCPU Xeon 2.1 GHz.

The spike implemented a Gost-DOM-shaped layer (`CreateClass` / `CreateOperation` / `CreateAttribute` / indexed + named handlers / `NewInstance` / promises) over a mini DOM (`EventTarget → Node → Element`, `Document`, `HTMLCollection`, `DOMStringMap`). **All 20 behaviour checks pass:**

- `div instanceof Element && Node && EventTarget`; `typeof Element === "function"`; `Object.getPrototypeOf(Element) === Node`
- `new Element()` throws `TypeError: Illegal constructor`; constructible classes work
- Attributes are **prototype accessors** (`div.hasOwnProperty('nodeName') === false`, getter on `Node.prototype`)
- Indexed handler: `children.length`, `children[1]`, out-of-range → `undefined`, `Array.from(children)`, `Object.keys(children)`
- Named handler: `dataset.userId = 42` round-trips through a Go map; `Object.keys(dataset)`; prototype methods still resolve
- Go `error` from a callback becomes a catchable JS `TypeError`
- Promise created with `JSObjectMakeDeferredPromise`, resolved from Go; `await` continuation runs (microtasks drained)
- Finalizers release Go objects: live Go handles stay bounded (~32.7k) after creating 3.2M Go-backed objects (JSC sweeps lazily, so no leak)
- 306 classes defined → still only **7 purego callbacks** (limit ~2000)
- **Parallel GC stress test passes**: 6 contexts on separate goroutines × 400k Go-backed DOM ops each, no crash

### Key design (how to avoid the purego callback limit and match Web IDL)

- **One `GoFunction` JSClass** with `callAsFunction` / `callAsConstructor` / `hasInstance` / `finalize`. Every Go-backed function (operations, getters, setters, constructors) is `JSObjectMake(goFunctionClass, handleID)`. One callback dispatches on `JSObjectGetPrivate(function)`.
- **Two instance JSClasses**: plain (finalize only), and "with handlers" (`getProperty` / `setProperty` / `getPropertyNames` / `finalize`) used only for interfaces with indexed or named handlers. Returning `NULL` from `getProperty` falls through to the prototype chain, so non-handler classes don't pay per-access callback cost.
- **Prototypes are ordinary JS objects** chained with `JSObjectSetPrototype`. Attributes and operations are installed with `Object.defineProperty`, giving real Web IDL shape. Root constructors get `Function.prototype` as their prototype.
- **JSC quirk:** `instanceof` on callback-object constructors **ignores `Symbol.hasInstance`** and calls the class `hasInstance` hook. The hook must delegate to `Function.prototype[Symbol.hasInstance]` (OrdinaryHasInstance), otherwise `instanceof` returns false.
- Go objects live in a handle table; the handle ID is the JS object's private data; `finalize` deletes it.

### Performance (same machine, 1M iterations)

| | V8 (cgo) | Sobek | JSC (purego) |
|---|---|---|---|
| `fib(30)` pure JS | 14 ms | 311 ms | **~15–19 ms** |
| Go accessor `div.nodeName` | 599 ns | 546 ns | 2,083 ns (3,900 before string interning) |
| Go method `div.getAttribute('id')` | 1,860 ns | 315 ns | ~6,100 ns |
| Empty Go method | 619 ns | 185 ns | ~1,900 ns |

Raw crossing cost on this machine: **purego ~310 ns per Go→C call / ~430 ns per C→Go callback, vs cgo ~66 / ~78 ns**. So JSC is ~20× faster than Sobek for pure JS, but each DOM boundary crossing is ~3× slower than V8 and ~10× slower than Sobek. For typical test workloads both are milliseconds, but **reducing crossings per call is the main optimisation target** (see checklist).

## Touch points in this repo

- `scripting/internal/js/`: the engine contract to implement (`ScriptEngine`, `Class`, `Constructor`, `GlobalObject`, `Value`, `Object` incl. `NativeValue`/`SetNativeValue`, `Function`, `Array`, `Promise`, `ValueFactory` incl. `NewIterator`/`NewUint8Array`/`NewTypeError`/`JSONParse`, `Scope`/`CallbackContext` incl. `Clock()`/`Eval`).
- `scripting/v8engine/` (~1.9k LOC) and `scripting/sobekengine/` (~1.5k LOC): reference adapters; each is its own Go module. A new `scripting/jscengine/` would mirror their file layout (`class.go`, `callback_context.go`, `value.go`, `object.go`, `function.go`, `promise`, `iterator`, `module`, `script_context.go`, `script_host.go`, `script_engine.go`).
- `html/window.go`: `html.ScriptEngine` → `ScriptHost` → `ScriptContext` (`Compile`, `DownloadScript`, **`DownloadModule`**) → `Script`.
- `scripting/internal/scripttests/`: engine-agnostic acceptance suites (`node_suite`, `element_suite`, `dataset_suite`, `event_loop_suite`, `error_handling_suite`, `modules_test_suite`, `htmx_suite`, `datastar_suite`, …). See how `v8engine` wires them (`scripttests_test.go`, `htmx_test.go`, `datastar_test.go`, `module_test.go`).
- `scripting/internal/polyfills/`: what `InstallPolyfill` loads.
- `v8browser/`: pattern for a convenience package (`jscbrowser/`).
- Note: the contract lives under `internal/`, so the adapter has to be **in-tree** (or the contract exported).

## Implementation checklist

### Must verify first (risks)
- [x] **Parallel contexts + GC signals without cgo — passes on Linux.** JSC's GC suspends threads with a signal (`SIGUSR1` by default) while Go owns signal handlers under `CGO_ENABLED=0`; JSC logs `Overriding existing handler for signal 10`. Stress test (`spike/stress_test.go`): 6 contexts on separate goroutines × 400k Go-backed DOM ops each, ~56 s on 1 vCPU, **no crash**, with both the default signal and `JSC_SIGNAL_FOR_GC=24`. Still to do: run under `-race`, and on macOS.
- [ ] **ES modules.** JSC's C API (and the GLib API) has **no module loading**. `DownloadModule` + `modules_test_suite` need a strategy. Candidate: bundle via esbuild's Go API (pure Go) with an `onResolve`/`onLoad` plugin that fetches through the browser's HTTP handler, then run as a classic script. Check live bindings, cyclic imports, top-level await (not supported in IIFE output) and error locations.
- [ ] **Object identity** (`el === el` for the same Go node). Needs a Go→JS object cache without leaking. The C API has no public weak refs; options are a JS-side `Map<handle, WeakRef>` or the private `JSWeakObjectMapRef`. Check what the V8/Sobek adapters do.
- [ ] **Unhandled rejections.** `JSGlobalContextSetUnhandledRejectionCallback` is in `JSContextRefPrivate.h`. Check the symbol is exported in WebKitGTK and macOS JavaScriptCore.framework.

### Engine contract
- [ ] `CreateClass` / `CreateOperation` / `CreateAttribute` / `CreateIndexedHandler` / `CreateNamedHandler` / `CreateIteratorMethod` (needs `Symbol.iterator` via `JSObjectSetPropertyForKey`, macOS 10.15+)
- [ ] `ConfigureGlobalScope` (Window as global; may need `JSGlobalContextCreate(globalClass)`)
- [ ] `Constructor.NewInstance`, `Object.NativeValue/SetNativeValue`
- [ ] Values: strings (cache/intern), numbers, `NewUint8Array` (`JSObjectMakeTypedArrayWithBytesNoCopy` or copy), arrays, iterators, JSON, errors with stack/location
- [ ] Promises (`JSObjectMakeDeferredPromise`), microtask timing vs the Gost-DOM `clock`
- [ ] Protect/unprotect (`JSValueProtect`) for JS values held by Go

### Platform loading
- [ ] Linux: `dlopen` fallback order `libjavascriptcoregtk-6.0.so.1` → `-4.1.so.0` → `-4.0.so.18`; clear error if missing; CI step `apt-get install libjavascriptcoregtk-6.0-1`
- [ ] macOS: load `/System/Library/Frameworks/JavaScriptCore.framework/JavaScriptCore`; check whether JIT works in unsigned `go test` binaries (Ramune signs with `com.apple.security.cs.allow-jit`), otherwise JSC falls back to its interpreter
- [ ] Windows: out of scope (no system JSC; purego `Dlopen` isn't available on Windows). Build-tag the package out.

### Performance
- [ ] Minimise crossings per call: intern strings, cache `undefined`/`null`/booleans, avoid duplicate `JSObjectGetPrivate`, avoid mutex-guarded handle lookups on the hot path
- [ ] Benchmark `htmx_suite` / `datastar_suite` end-to-end vs v8engine and sobekengine

### Acceptance
- [ ] `scripting/jscengine` passes the same `scripttests` suites as `sobekengine`, at minimum: node, element, dataset, error handling, event loop, htmx, datastar
- [ ] Builds and tests with `CGO_ENABLED=0` on Linux and macOS
- [ ] Parallel test runs are stable

## Alternatives considered

- **fastschema/qjs** (QuickJS-NG on wazero): pure Go and runs on Windows, but its Go API lacks class/prototype creation, getter/setter definitions, indexed/named handlers, finalizers and a rejection hook. Speed is roughly Sobek-level, so it would add language coverage, not performance.
- **V8 via purego:** V8's API is C++, so it would need a C shim shipped as a shared library per platform. Much more work than JSC, which already has a C API.
- **[i2y/ramune](https://github.com/i2y/ramune):** already loads JSC via purego (good reference for loading and signals), but its public API doesn't expose class inheritance or property handlers, so it's not usable as a dependency here.

## Questions for maintainers

1. Would you accept `scripting/jscengine` in-tree (given the `internal/` contract)?
2. Any parts of the contract that are harder than they look, e.g. global scope / event loop?
3. How do v8engine/sobekengine handle object identity and module loading today? Worth mirroring?

Happy to help drive this.
