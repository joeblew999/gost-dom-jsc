package main

import (
	"strconv"
	"unsafe"

	"github.com/ebitengine/purego"
)

// A tiny Gost-DOM-shaped engine layer over JSC.
//
// Trick: every Go-backed function is an instance of ONE JSClass ("GoFunction")
// whose callAsFunction callback dispatches on the object's private handle.
// Every Go-backed DOM object is an instance of one of TWO JSClasses: plain, or
// "with handlers" (indexed/named). Prototype chains are ordinary JS objects.
// => a fixed, small number of purego callbacks no matter how big the DOM is.

type Callback func(c *Call) (valRef, error)

type Call struct {
	Ctx  ctxRef
	This objRef
	Args []valRef
	Inst *Instance // Go side of `this`, if any
}

type goFunc struct {
	name string
	call Callback
	ctor bool // may be used with `new`
}

type Class struct {
	Name      string
	Proto     objRef
	Ctor      objRef
	Parent    *Class
	Indexed   func(inst *Instance, i int) (valRef, bool)
	Length    func(inst *Instance) int
	NamedGet  func(inst *Instance, name string) (valRef, bool)
	NamedSet  func(inst *Instance, name string, v valRef) bool
	NamedKeys func(inst *Instance) []string
}

type Instance struct {
	Class  *Class
	Native any
}

type Engine struct {
	Ctx                            ctxRef
	fnClass, objPlain, objHandlers classRef
	ordinaryHasInstance            objRef
	undef                          valRef
	strCache                       map[string]valRef
	classes                        map[string]*Class
}

var callbackCount int

func cb(fn any) uintptr { callbackCount++; return purego.NewCallback(fn) }

var (
	cbCall, cbConstruct, cbFinalize, cbGet, cbSet, cbNames, cbHasInstance uintptr
	engines                                                               = map[ctxRef]*Engine{}
)

func setupCallbacks() {
	cbCall = cb(func(ctx, function, this, argc, argv, exc uintptr) uintptr {
		f := handles.get(JSObjectGetPrivate(function)).(*goFunc)
		return invoke(ctx, f, this, argc, argv, exc)
	})
	cbConstruct = cb(func(ctx, ctor, argc, argv, exc uintptr) uintptr {
		f := handles.get(JSObjectGetPrivate(ctor)).(*goFunc)
		if !f.ctor {
			throwTypeError(ctx, exc, "Illegal constructor")
			return 0
		}
		return invoke(ctx, f, 0, argc, argv, exc)
	})
	// JSC routes `instanceof` on callback-object constructors to this hook
	// (it ignores Symbol.hasInstance), so delegate to OrdinaryHasInstance.
	cbHasInstance = cb(func(ctx, ctor, candidate, exc uintptr) uintptr {
		en := engines[ctx]
		r := JSObjectCallAsFunction(ctx, en.ordinaryHasInstance, ctor, 1, &candidate, nil)
		if JSValueToBoolean(ctx, r) {
			return 1
		}
		return 0
	})
	cbFinalize = cb(func(obj uintptr) uintptr {
		if id := JSObjectGetPrivate(obj); id != 0 {
			handles.del(id)
		}
		return 0
	})
	// Indexed + named handlers. Returning 0 = "not mine", JSC falls through
	// to the prototype chain, so methods/attributes still resolve normally.
	cbGet = cb(func(ctx, obj, name, exc uintptr) uintptr {
		inst, _ := handles.get(JSObjectGetPrivate(obj)).(*Instance)
		if inst == nil {
			return 0
		}
		key := goStr(name)
		c := inst.Class
		for k := c; k != nil; k = k.Parent {
			if k.Indexed != nil {
				if i, err := strconv.Atoi(key); err == nil {
					if v, ok := k.Indexed(inst, i); ok {
						return v
					}
					return JSValueMakeUndefined(ctx)
				}
			}
			if key == "length" && k.Length != nil {
				return JSValueMakeNumber(ctx, float64(k.Length(inst)))
			}
			if k.NamedGet != nil {
				if v, ok := k.NamedGet(inst, key); ok {
					return v
				}
			}
		}
		return 0
	})
	cbSet = cb(func(ctx, obj, name, value, exc uintptr) uintptr {
		inst, _ := handles.get(JSObjectGetPrivate(obj)).(*Instance)
		if inst == nil {
			return 0
		}
		for k := inst.Class; k != nil; k = k.Parent {
			if k.NamedSet != nil && k.NamedSet(inst, goStr(name), value) {
				return 1
			}
		}
		return 0
	})
	cbNames = cb(func(ctx, obj, acc uintptr) uintptr {
		inst, _ := handles.get(JSObjectGetPrivate(obj)).(*Instance)
		if inst == nil {
			return 0
		}
		for k := inst.Class; k != nil; k = k.Parent {
			if k.Length != nil {
				for i := 0; i < k.Length(inst); i++ {
					s := JSStringCreateWithUTF8CString(strconv.Itoa(i))
					JSPropertyNameAccumulatorAddName(acc, s)
					JSStringRelease(s)
				}
			}
			if k.NamedKeys != nil {
				for _, n := range k.NamedKeys(inst) {
					s := JSStringCreateWithUTF8CString(n)
					JSPropertyNameAccumulatorAddName(acc, s)
					JSStringRelease(s)
				}
			}
		}
		return 0
	})
}

func invoke(ctx ctxRef, f *goFunc, this objRef, argc, argv, exc uintptr) uintptr {
	c := &Call{Ctx: ctx, This: this}
	if argc > 0 {
		c.Args = unsafe.Slice((*valRef)(unsafe.Pointer(argv)), argc)
	}
	if this != 0 {
		c.Inst, _ = handles.get(JSObjectGetPrivate(this)).(*Instance)
	}
	v, err := f.call(c)
	if err != nil {
		throwTypeError(ctx, exc, err.Error())
		return 0
	}
	if v == 0 {
		return engines[ctx].undef
	}
	return v
}

func throwTypeError(ctx ctxRef, exc uintptr, msg string) {
	e := engines[ctx]
	te := e.global("TypeError")
	s := e.str(msg)
	errObj := JSObjectCallAsConstructor(ctx, te, 1, &s, nil)
	*(*valRef)(unsafe.Pointer(exc)) = errObj
}

func NewEngine() *Engine {
	e := &Engine{classes: map[string]*Class{}}
	e.fnClass = JSClassCreate(&classDef{ClassName: cstr("GoFunction"),
		CallAsFunction: cbCall, CallAsConstructor: cbConstruct, HasInstance: cbHasInstance, Finalize: cbFinalize})
	e.objPlain = JSClassCreate(&classDef{ClassName: cstr("GoObject"), Finalize: cbFinalize})
	e.objHandlers = JSClassCreate(&classDef{ClassName: cstr("GoObjectWithHandlers"),
		Finalize: cbFinalize, GetProperty: cbGet, SetProperty: cbSet, GetPropertyNames: cbNames})
	e.Ctx = JSGlobalContextCreate(0)
	engines[e.Ctx] = e
	e.undef = JSValueMakeUndefined(e.Ctx)
	e.strCache = map[string]valRef{}
	e.ordinaryHasInstance = JSValueToObject(e.Ctx, e.Eval("Function.prototype[Symbol.hasInstance]"), nil)
	return e
}

func (e *Engine) istr(s string) valRef {
	if v, ok := e.strCache[s]; ok {
		return v
	}
	v := e.str(s)
	JSValueProtect(e.Ctx, v)
	e.strCache[s] = v
	return v
}

func (e *Engine) str(s string) valRef {
	r := JSStringCreateWithUTF8CString(s)
	defer JSStringRelease(r)
	return JSValueMakeString(e.Ctx, r)
}

func (e *Engine) global(name string) objRef {
	n := JSStringCreateWithUTF8CString(name)
	defer JSStringRelease(n)
	v := JSObjectGetProperty(e.Ctx, JSContextGetGlobalObject(e.Ctx), n, nil)
	return JSValueToObject(e.Ctx, v, nil)
}

func (e *Engine) NewFunction(name string, call Callback, ctor bool) objRef {
	return JSObjectMake(e.Ctx, e.fnClass, handles.add(&goFunc{name: name, call: call, ctor: ctor}))
}

// defineAccessor uses Object.defineProperty so attributes live on the
// prototype as real accessors, exactly like Web IDL / real browsers.
func (e *Engine) define(target objRef, name string, desc map[string]valRef) {
	obj := e.Eval("({})")
	o := JSValueToObject(e.Ctx, obj, nil)
	for k, v := range desc {
		n := JSStringCreateWithUTF8CString(k)
		JSObjectSetProperty(e.Ctx, o, n, v, 0, nil)
		JSStringRelease(n)
	}
	defineProperty := JSValueToObject(e.Ctx, e.Eval("Object.defineProperty"), nil)
	args := []valRef{target, e.str(name), o}
	var exc valRef
	JSObjectCallAsFunction(e.Ctx, defineProperty, 0, 3, &args[0], &exc)
	must(exc, e.Ctx)
}

// CreateClass mirrors ScriptEngine.CreateClass(name, parent, ctor).
func (e *Engine) CreateClass(name string, parent *Class, ctor Callback) *Class {
	c := &Class{Name: name, Parent: parent}
	c.Proto = JSValueToObject(e.Ctx, e.Eval("({})"), nil)
	if parent != nil {
		JSObjectSetPrototype(e.Ctx, c.Proto, parent.Proto)
	}
	c.Ctor = e.NewFunction(name, func(call *Call) (valRef, error) {
		if ctor == nil {
			return 0, errIllegal
		}
		return ctor(call)
	}, ctor != nil)
	e.define(c.Ctor, "prototype", map[string]valRef{"value": c.Proto})
	e.define(c.Proto, "constructor", map[string]valRef{"value": c.Ctor})
	if parent != nil {
		JSObjectSetPrototype(e.Ctx, c.Ctor, parent.Ctor) // static inheritance
	} else {
		// Root interfaces inherit from Function.prototype, like real browsers.
		// This also supplies Symbol.hasInstance, so instanceof works.
		JSObjectSetPrototype(e.Ctx, c.Ctor, e.Eval("Function.prototype"))
	}
	tag := e.Eval("Symbol.toStringTag")
	_ = tag
	n := JSStringCreateWithUTF8CString(name)
	JSObjectSetProperty(e.Ctx, JSContextGetGlobalObject(e.Ctx), n, c.Ctor, 4 /*DontEnum*/, nil)
	JSStringRelease(n)
	e.classes[name] = c
	return c
}

func (e *Engine) CreateOperation(c *Class, name string, call Callback) {
	e.define(c.Proto, name, map[string]valRef{"value": e.NewFunction(name, call, false),
		"writable": e.Eval("true"), "configurable": e.Eval("true")})
}

func (e *Engine) CreateAttribute(c *Class, name string, get, set Callback) {
	d := map[string]valRef{"get": e.NewFunction("get "+name, get, false),
		"configurable": e.Eval("true"), "enumerable": e.Eval("true")}
	if set != nil {
		d["set"] = e.NewFunction("set "+name, set, false)
	}
	e.define(c.Proto, name, d)
}

// NewInstance mirrors Constructor.NewInstance(nativeValue).
func (e *Engine) NewInstance(c *Class, native any) objRef {
	cls := e.objPlain
	for k := c; k != nil; k = k.Parent {
		if k.Indexed != nil || k.NamedGet != nil {
			cls = e.objHandlers // only pay the per-access callback cost when needed
		}
	}
	o := JSObjectMake(e.Ctx, cls, handles.add(&Instance{Class: c, Native: native}))
	JSObjectSetPrototype(e.Ctx, o, c.Proto)
	return o
}

func (e *Engine) Eval(src string) valRef {
	s := JSStringCreateWithUTF8CString(src)
	defer JSStringRelease(s)
	var exc valRef
	v := JSEvaluateScript(e.Ctx, s, 0, 0, 1, &exc)
	must(exc, e.Ctx)
	return v
}

func (e *Engine) EvalString(src string) string {
	v := e.Eval(src)
	s := JSValueToStringCopy(e.Ctx, v, nil)
	defer JSStringRelease(s)
	return goStr(s)
}

type strErr string

func (s strErr) Error() string { return string(s) }

const errIllegal = strErr("Illegal constructor")
