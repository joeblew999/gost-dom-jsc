package main

// Minimal purego binding to JavaScriptCore's C API — no cgo.

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type (
	ctxRef   = uintptr
	valRef   = uintptr
	objRef   = uintptr
	strRef   = uintptr
	classRef = uintptr
)

var (
	JSGlobalContextCreate             func(class classRef) ctxRef
	JSGlobalContextRelease            func(ctx ctxRef)
	JSContextGetGlobalObject          func(ctx ctxRef) objRef
	JSEvaluateScript                  func(ctx ctxRef, script strRef, this objRef, url strRef, line int32, exc *valRef) valRef
	JSStringCreateWithUTF8CString     func(s string) strRef
	JSStringRelease                   func(s strRef)
	JSStringGetMaximumUTF8CStringSize func(s strRef) uintptr
	JSStringGetUTF8CString            func(s strRef, buf *byte, size uintptr) uintptr
	JSClassCreate                     func(def *classDef) classRef
	JSObjectMake                      func(ctx ctxRef, class classRef, data uintptr) objRef
	JSObjectGetPrivate                func(obj objRef) uintptr
	JSObjectSetPrototype              func(ctx ctxRef, obj objRef, proto valRef)
	JSObjectSetProperty               func(ctx ctxRef, obj objRef, name strRef, v valRef, attrs uint32, exc *valRef)
	JSObjectGetProperty               func(ctx ctxRef, obj objRef, name strRef, exc *valRef) valRef
	JSObjectCallAsFunction            func(ctx ctxRef, fn objRef, this objRef, argc uintptr, argv *valRef, exc *valRef) valRef
	JSObjectCallAsConstructor         func(ctx ctxRef, fn objRef, argc uintptr, argv *valRef, exc *valRef) objRef
	JSObjectMakeDeferredPromise       func(ctx ctxRef, resolve *objRef, reject *objRef, exc *valRef) objRef
	JSValueMakeUndefined              func(ctx ctxRef) valRef
	JSValueMakeNumber                 func(ctx ctxRef, n float64) valRef
	JSValueMakeString                 func(ctx ctxRef, s strRef) valRef
	JSValueToStringCopy               func(ctx ctxRef, v valRef, exc *valRef) strRef
	JSValueToNumber                   func(ctx ctxRef, v valRef, exc *valRef) float64
	JSValueToObject                   func(ctx ctxRef, v valRef, exc *valRef) objRef
	JSPropertyNameAccumulatorAddName  func(acc uintptr, name strRef)
	JSGarbageCollect                  func(ctx ctxRef)
	JSValueToBoolean                  func(ctx ctxRef, v valRef) bool
	JSValueProtect                    func(ctx ctxRef, v valRef)
)

// classDef mirrors JSClassDefinition.
type classDef struct {
	Version, Attributes                   int32
	ClassName, ParentClass                uintptr
	StaticValues, StaticFunctions         uintptr
	Initialize, Finalize                  uintptr
	HasProperty, GetProperty, SetProperty uintptr
	DeleteProperty, GetPropertyNames      uintptr
	CallAsFunction, CallAsConstructor     uintptr
	HasInstance, ConvertToType            uintptr
}

func load() {
	lib, err := purego.Dlopen("libjavascriptcoregtk-6.0.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	reg := func(fptr any, name string) { purego.RegisterLibFunc(fptr, lib, name) }
	reg(&JSGlobalContextCreate, "JSGlobalContextCreate")
	reg(&JSGlobalContextRelease, "JSGlobalContextRelease")
	reg(&JSContextGetGlobalObject, "JSContextGetGlobalObject")
	reg(&JSEvaluateScript, "JSEvaluateScript")
	reg(&JSStringCreateWithUTF8CString, "JSStringCreateWithUTF8CString")
	reg(&JSStringRelease, "JSStringRelease")
	reg(&JSStringGetMaximumUTF8CStringSize, "JSStringGetMaximumUTF8CStringSize")
	reg(&JSStringGetUTF8CString, "JSStringGetUTF8CString")
	reg(&JSClassCreate, "JSClassCreate")
	reg(&JSObjectMake, "JSObjectMake")
	reg(&JSObjectGetPrivate, "JSObjectGetPrivate")
	reg(&JSObjectSetPrototype, "JSObjectSetPrototype")
	reg(&JSObjectSetProperty, "JSObjectSetProperty")
	reg(&JSObjectGetProperty, "JSObjectGetProperty")
	reg(&JSObjectCallAsFunction, "JSObjectCallAsFunction")
	reg(&JSObjectCallAsConstructor, "JSObjectCallAsConstructor")
	reg(&JSObjectMakeDeferredPromise, "JSObjectMakeDeferredPromise")
	reg(&JSValueMakeUndefined, "JSValueMakeUndefined")
	reg(&JSValueMakeNumber, "JSValueMakeNumber")
	reg(&JSValueMakeString, "JSValueMakeString")
	reg(&JSValueToStringCopy, "JSValueToStringCopy")
	reg(&JSValueToNumber, "JSValueToNumber")
	reg(&JSValueToObject, "JSValueToObject")
	reg(&JSPropertyNameAccumulatorAddName, "JSPropertyNameAccumulatorAddName")
	reg(&JSGarbageCollect, "JSGarbageCollect")
	reg(&JSValueToBoolean, "JSValueToBoolean")
	reg(&JSValueProtect, "JSValueProtect")
}

func goStr(s strRef) string {
	n := JSStringGetMaximumUTF8CStringSize(s)
	buf := make([]byte, n)
	w := JSStringGetUTF8CString(s, &buf[0], n)
	if w == 0 {
		return ""
	}
	return string(buf[:w-1])
}

func cstr(s string) uintptr {
	b := append([]byte(s), 0)
	pinned = append(pinned, b) // keep alive for class lifetime
	return uintptr(unsafe.Pointer(&b[0]))
}

var pinned [][]byte

// ---- handle registry: JS objects carry an integer handle in private data ----

type handleTable struct {
	mu   sync.Mutex
	next uintptr
	m    map[uintptr]any
}

func (h *handleTable) add(v any) uintptr {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	h.m[h.next] = v
	return h.next
}
func (h *handleTable) get(id uintptr) any { h.mu.Lock(); defer h.mu.Unlock(); return h.m[id] }
func (h *handleTable) del(id uintptr)     { h.mu.Lock(); defer h.mu.Unlock(); delete(h.m, id) }
func (h *handleTable) len() int           { h.mu.Lock(); defer h.mu.Unlock(); return len(h.m) }

var handles = &handleTable{m: map[uintptr]any{}}

func init() { runtime.LockOSThread() }

func must(exc valRef, ctx ctxRef) {
	if exc != 0 {
		s := JSValueToStringCopy(ctx, exc, nil)
		panic(fmt.Sprintf("JS exception: %s", goStr(s)))
	}
}
