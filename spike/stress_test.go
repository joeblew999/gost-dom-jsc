package main

import (
	"runtime"
	"sync"
	"testing"
)

func TestParallelGCStress(t *testing.T) {
	load()
	setupCallbacks()
	es := make([]*Engine, 6)
	for i := range es {
		es[i] = NewEngine()
		buildDOM(es[i])
	}
	var wg sync.WaitGroup
	for i, e := range es {
		wg.Add(1)
		go func(i int, e *Engine) {
			defer wg.Done()
			runtime.LockOSThread()
			for round := 0; round < 20; round++ {
				e.Eval(`for (let i=0;i<20000;i++){ const d=document.createElement('div'); d.dataset.k=i; d.nodeName; [1,2,3].map(x=>({x})) }`)
			}
		}(i, e)
	}
	wg.Wait()
	t.Logf("6 contexts x 400k Go-backed DOM ops each, in parallel: OK, live handles=%d", handles.len())
}
