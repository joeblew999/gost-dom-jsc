package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

type node struct {
	name     string
	children []*node
	attrs    map[string]string
	data     map[string]string
}

func newNode(n string) *node {
	return &node{name: n, attrs: map[string]string{}, data: map[string]string{}}
}

func check(label string, got, want string) {
	mark := "PASS"
	if got != want {
		mark = "FAIL"
	}
	fmt.Printf("  [%s] %-58s => %s\n", mark, label, got)
}

func buildDOM(e *Engine) {
	str := e.str
	et := e.CreateClass("EventTarget", nil, func(c *Call) (valRef, error) {
		return e.NewInstance(e.classes["EventTarget"], nil), nil
	})
	nodeC := e.CreateClass("Node", et, nil)
	e.CreateAttribute(nodeC, "nodeName", func(c *Call) (valRef, error) {
		return e.istr(c.Inst.Native.(*node).name), nil
	}, nil)
	el := e.CreateClass("Element", nodeC, nil)
	coll := e.CreateClass("HTMLCollection", nil, nil)
	coll.Length = func(i *Instance) int { return len(i.Native.(*node).children) }
	coll.Indexed = func(i *Instance, idx int) (valRef, bool) {
		ch := i.Native.(*node).children
		if idx < 0 || idx >= len(ch) {
			return 0, false
		}
		return e.NewInstance(el, ch[idx]), true
	}
	dsm := e.CreateClass("DOMStringMap", nil, nil)
	dsm.NamedGet = func(i *Instance, k string) (valRef, bool) {
		v, ok := i.Native.(*node).data[k]
		if !ok {
			return 0, false
		}
		return str(v), true
	}
	dsm.NamedSet = func(i *Instance, k string, v valRef) bool {
		s := JSValueToStringCopy(e.Ctx, v, nil)
		i.Native.(*node).data[k] = goStr(s)
		JSStringRelease(s)
		return true
	}
	dsm.NamedKeys = func(i *Instance) (ks []string) {
		for k := range i.Native.(*node).data {
			ks = append(ks, k)
		}
		return
	}
	e.CreateAttribute(el, "children", func(c *Call) (valRef, error) {
		return e.NewInstance(coll, c.Inst.Native), nil
	}, nil)
	e.CreateAttribute(el, "dataset", func(c *Call) (valRef, error) {
		return e.NewInstance(dsm, c.Inst.Native), nil
	}, nil)
	e.CreateOperation(el, "getAttribute", func(c *Call) (valRef, error) {
		if len(c.Args) < 1 {
			return 0, strErr("Failed to execute 'getAttribute': 1 argument required")
		}
		s := JSValueToStringCopy(c.Ctx, c.Args[0], nil)
		defer JSStringRelease(s)
		return str(c.Inst.Native.(*node).attrs[goStr(s)]), nil
	})
	e.CreateOperation(el, "appendChild", func(c *Call) (valRef, error) {
		child, _ := handles.get(JSObjectGetPrivate(c.Args[0])).(*Instance)
		p := c.Inst.Native.(*node)
		p.children = append(p.children, child.Native.(*node))
		return c.Args[0], nil
	})
	doc := e.CreateClass("Document", nodeC, nil)
	e.CreateOperation(doc, "createElement", func(c *Call) (valRef, error) {
		s := JSValueToStringCopy(c.Ctx, c.Args[0], nil)
		defer JSStringRelease(s)
		n := newNode(goStr(s))
		n.attrs["id"] = "x-" + n.name
		return e.NewInstance(el, n), nil
	})
	d := e.NewInstance(doc, newNode("#document"))
	n := JSStringCreateWithUTF8CString("document")
	JSObjectSetProperty(e.Ctx, JSContextGetGlobalObject(e.Ctx), n, d, 0, nil)
	JSStringRelease(n)
}

func main() {
	load()
	setupCallbacks()
	e := NewEngine()
	buildDOM(e)

	fmt.Println("1. Classes, inheritance, instanceof (Web IDL shape)")
	e.Eval(`var div = document.createElement('div'); div.appendChild(document.createElement('span')); div.appendChild(document.createElement('p'));`)
	check("div instanceof Element && Node && EventTarget", e.EvalString(`div instanceof Element && div instanceof Node && div instanceof EventTarget`), "true")
	check("typeof Element", e.EvalString(`typeof Element`), "function")
	check("prototype chain names", e.EvalString(`[Object.getPrototypeOf(div)===Element.prototype, Object.getPrototypeOf(Element.prototype)===Node.prototype].join()`), "true,true")
	check("Element.__proto__ === Node (static inheritance)", e.EvalString(`Object.getPrototypeOf(Element)===Node`), "true")
	check("new Element() throws TypeError", e.EvalString(`try{new Element();'no throw'}catch(x){x instanceof TypeError ? x.message : 'wrong'}`), "Illegal constructor")
	check("new EventTarget() allowed", e.EvalString(`new EventTarget() instanceof EventTarget`), "true")

	fmt.Println("2. Attributes are prototype accessors (like real browsers)")
	check("div.nodeName", e.EvalString(`div.nodeName`), "div")
	check("own property? (should be false)", e.EvalString(`div.hasOwnProperty('nodeName')`), "false")
	check("descriptor on Node.prototype has getter", e.EvalString(`typeof Object.getOwnPropertyDescriptor(Node.prototype,'nodeName').get`), "function")

	fmt.Println("3. Indexed handler (HTMLCollection)")
	check("div.children.length", e.EvalString(`div.children.length`), "2")
	check("div.children[1].nodeName", e.EvalString(`div.children[1].nodeName`), "p")
	check("div.children[9] (out of range)", e.EvalString(`String(div.children[9])`), "undefined")
	check("Array.from(div.children) names", e.EvalString(`Array.from(div.children).map(c=>c.nodeName).join()`), "span,p")
	check("Object.keys(div.children)", e.EvalString(`Object.keys(div.children).join()`), "0,1")

	fmt.Println("4. Named handler (DOMStringMap / dataset)")
	e.Eval(`div.dataset.userId = 42`)
	check("div.dataset.userId (round trip via Go map)", e.EvalString(`div.dataset.userId`), "42")
	check("Object.keys(div.dataset)", e.EvalString(`Object.keys(div.dataset).join()`), "userId")
	check("methods still resolve through prototype", e.EvalString(`div.getAttribute('id')`), "x-div")

	fmt.Println("5. Go errors become catchable JS TypeErrors")
	check("div.getAttribute() with no args", e.EvalString(`try{div.getAttribute()}catch(x){x.constructor.name+': '+x.message}`), "TypeError: Failed to execute 'getAttribute': 1 argument required")

	fmt.Println("6. Promise created and resolved from Go")
	var resolve, reject objRef
	p := JSObjectMakeDeferredPromise(e.Ctx, &resolve, &reject, nil)
	g := JSStringCreateWithUTF8CString("pending")
	JSObjectSetProperty(e.Ctx, JSContextGetGlobalObject(e.Ctx), g, p, 0, nil)
	e.Eval(`globalThis.out = 'not yet'; (async () => { out = 'got ' + (await pending) })()`)
	check("before Go resolves", e.EvalString(`out`), "not yet")
	v := e.str("hello from Go")
	JSObjectCallAsFunction(e.Ctx, resolve, 0, 1, &v, nil)
	check("after Go resolves (microtasks drained)", e.EvalString(`out`), "got hello from Go")

	fmt.Println("7. Finalizers release Go objects")
	before := handles.len()
	e.Eval(`for (let i=0;i<200000;i++){ document.createElement('div').dataset }`)
	peak := handles.len()
	for i := 0; i < 3; i++ {
		JSGarbageCollect(e.Ctx)
		time.Sleep(50 * time.Millisecond)
	}
	e.Eval(`1`)
	after := handles.len()
	// JSC sweeps lazily: finalizers run when the heap is reused. Churn plain JS allocations, then GC again.
	e.Eval(`for (let i=0;i<300000;i++){ ({a:i}) }`)
	JSGarbageCollect(e.Ctx)
	e.Eval(`for (let i=0;i<300000;i++){ ({a:i}) }`)
	afterChurn := handles.len()
	fmt.Printf("  created 200000 elements+datasets; live Go handles after GC+churn=%d\n", afterChurn)
	for round := 1; round <= 3; round++ {
		e.Eval(`for (let i=0;i<500000;i++){ document.createElement('div').dataset }`)
		JSGarbageCollect(e.Ctx)
		fmt.Printf("  after another 1M Go-backed objects (round %d): live Go handles=%d\n", round, handles.len())
	}
	fmt.Printf("  live Go handles: before=%d peak=%d after GC=%d\n", before, peak, after)

	fmt.Println("8. purego callback budget")
	for i := 0; i < 300; i++ {
		c := e.CreateClass(fmt.Sprintf("Extra%d", i), nil, nil)
		e.CreateOperation(c, "op", func(*Call) (valRef, error) { return 0, nil })
		e.CreateAttribute(c, "attr", func(*Call) (valRef, error) { return 0, nil }, nil)
	}
	fmt.Printf("  classes defined: %d, purego callbacks used: %d (limit ~2000)\n", len(e.classes), callbackCount)

	fmt.Println("9. Speed (JSC JIT, this container)")
	t := time.Now()
	e.Eval(`function fib(n){return n<2?n:fib(n-1)+fib(n-2)}; fib(30)`)
	fmt.Printf("  fib(30) pure JS:                    %v\n", time.Since(t).Round(time.Millisecond))
	const N = 1_000_000
	t = time.Now()
	e.Eval(`var s=0; for (let i=0;i<1e6;i++){ s += div.nodeName.length }`)
	fmt.Printf("  div.nodeName (Go accessor) x1e6:    %v  (%.0f ns/call)\n", time.Since(t).Round(time.Millisecond), float64(time.Since(t).Nanoseconds())/N)
	t = time.Now()
	e.Eval(`var g=div.getAttribute; for (let i=0;i<1e6;i++){ div.getAttribute('id') }`)
	fmt.Printf("  div.getAttribute('id') x1e6:        %v  (%.0f ns/call)\n", time.Since(t).Round(time.Millisecond), float64(time.Since(t).Nanoseconds())/N)

	e.CreateOperation(e.classes["Element"], "noop", func(*Call) (valRef, error) { return 0, nil })
	t = time.Now()
	e.Eval(`for (let i=0;i<1e6;i++){ div.noop() }`)
	fmt.Printf("  div.noop() (empty Go callback) x1e6: %v  (%.0f ns/call)\n", time.Since(t).Round(time.Millisecond), float64(time.Since(t).Nanoseconds())/N)
	t = time.Now()
	for i := 0; i < N; i++ {
		JSValueMakeNumber(e.Ctx, 1)
	}
	fmt.Printf("  Go->JSC purego call (JSValueMakeNumber) x1e6: %v  (%.0f ns/call)\n", time.Since(t).Round(time.Millisecond), float64(time.Since(t).Nanoseconds())/N)
	fmt.Println("10. Parallel contexts on separate goroutines")
	var wg sync.WaitGroup
	es := make([]*Engine, 4)
	for i := range es {
		es[i] = NewEngine()
	}
	t = time.Now()
	res := make([]string, len(es))
	for i, ee := range es {
		wg.Add(1)
		go func(i int, ee *Engine) {
			defer wg.Done()
			runtime.LockOSThread()
			res[i] = ee.EvalString(`function fib(n){return n<2?n:fib(n-1)+fib(n-2)}; fib(27)`)
		}(i, ee)
	}
	wg.Wait()
	fmt.Printf("  4 contexts in parallel: %v in %v\n", res, time.Since(t).Round(time.Millisecond))
}
