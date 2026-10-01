<!-- 2026-10-01-02-agent-handoff.md -->

# Agent handoff: cgo-free JavaScriptCore engine for Gost-DOM

## Goal

Add a JavaScriptCore (JSC) script engine to [gost-dom/browser](https://github.com/gost-dom/browser), loaded with [purego](https://github.com/ebitengine/purego) so it builds with `CGO_ENABLED=0`, alongside the existing `v8engine` (cgo) and `sobekengine` (pure Go, no JIT).

Full proposal, design notes, benchmarks and checklist: `docs/2026-10-01-01-gost-dom-jsc-purego-issue.md` (also filed as an issue in this repo).

## What's proven (in `spike/`)

- 20/20 Web-IDL behaviour checks pass: classes, inheritance, `instanceof`, prototype accessors, indexed + named handlers, Go errors → JS `TypeError`, promises from Go, finalizers.
- 306 classes use only 7 purego callbacks (one shared `GoFunction` JSClass + two instance JSClasses, dispatch via private data).
- **Parallel GC stress test passes**: 6 contexts on separate goroutines × 400k Go-backed DOM ops each, ~55 s on 1 vCPU, no crash, with JSC's default GC signal and with `JSC_SIGNAL_FOR_GC=24`. Not yet run under `-race`.
- JS speed ≈ V8; each Go↔JS crossing ≈ 3× slower than V8's cgo.

## Run the spike

The library is picked per OS in `libNames()` (`jsc.go`): JavaScriptCore.framework on macOS, `libjavascriptcoregtk` 6.0 → 4.1 → 4.0 on Linux.

```
# Linux only: sudo apt-get install libjavascriptcoregtk-6.0-1
cd spike
CGO_ENABLED=0 go run .            # exits non-zero if any check fails
CGO_ENABLED=0 go test -run TestParallelGCStress -v .
```

CI (`.github/workflows/spike.yml`) runs both on `ubuntu-latest` and `macos-latest` on every push. Check the macOS job first: it answers whether the spike works on macOS and whether the JIT is active (look at `fib(30)`: ~15 ms = JIT, much slower = interpreter).

## Testing on other OSes: irgo-windows-vm

[joeblew999/irgo-windows-vm](https://github.com/joeblew999/irgo-windows-vm) controls macOS and Windows guests through UTM, has an MCP server, and is cgo-free like this repo. Use it to run the spike and, later, the `jscengine` acceptance suites on OSes other than the host (clean macOS guest, Linux guest, Windows for comparing the other engines).

The two repos inform each other through issues:
- What this repo needs from irgo: [joeblew999/irgo-windows-vm#4](https://github.com/joeblew999/irgo-windows-vm/issues/4). Add to it when a new need comes up; don't build VM tooling here.
- irgo is still being updated, so read its `CLAUDE.md` / `AGENTS.md` for the current way to drive guests rather than assuming.

## Next steps, in order

1. Check the macOS CI result. If it fails, fix on the host Mac. Then use a clean macOS guest via irgo to confirm the JIT in an unsigned binary.
2. Run the stress test with `-race` (expect it to be slow).
3. Post the proposal upstream: `gh issue create -R gost-dom/browser --title "Proposal: cgo-free JavaScriptCore engine via purego (spike results + implementation checklist)" --body-file docs/2026-10-01-01-gost-dom-jsc-purego-issue.md`
4. `gh repo fork gost-dom/browser --clone`, then work the checklist in the proposal: remaining risks first (ES modules, object identity, unhandled rejections), then `scripting/jscengine/` mirroring `scripting/sobekengine/`, then the `scripttests` acceptance suites, run on Linux and macOS via irgo.

## Key gotchas

- JSC ignores `Symbol.hasInstance` on callback-object constructors and calls the class `hasInstance` hook instead. The hook must delegate to `Function.prototype[Symbol.hasInstance]`.
- JSC sweeps lazily, so finalizers lag. Don't treat a rising handle count as a leak until after churn + GC.
- JSC logs `Overriding existing handler for signal 10`. Harmless so far.
- The Gost-DOM engine contract is under `scripting/internal/js/`, so the adapter must live in-tree in the fork.

## Spike shortcuts: don't copy these into `jscengine`

- **`engines` map has no lock.** It maps JSC context → engine and is read from every callback. The stress test only works because all engines are created before the goroutines start. Use a lock, or store the engine pointer somewhere reachable from the context (e.g. private data on the global object).
- **One global handle table behind one mutex.** Every callback locks it, which adds to the crossing cost and serialises parallel tests. Use per-engine tables.
- **`throwTypeError` looks up `TypeError` on every throw** and `define()` re-evaluates `Object.defineProperty` and `true` on every call. Cache these per engine.
- **`go vet` warns "possible misuse of unsafe.Pointer"** where C pointers arrive as `uintptr` (`argv`, `exc`). Expected with purego callbacks; give the real code typed pointer parameters where purego allows it.
- **No `JSGlobalContextRelease` / unprotect** anywhere. The real adapter needs a clean shutdown path per context.
