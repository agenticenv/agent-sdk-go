package store

import (
	"sync"
	"testing"
)

func TestNewKV_Empty(t *testing.T) {
	kv := NewKV[string, int]()
	if kv == nil {
		t.Fatal("NewKV returned nil")
	}
	if got := kv.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	if _, ok := kv.Get("missing"); ok {
		t.Fatal("Get on empty store returned ok=true")
	}
}

func TestKV_SetGet(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)

	got, ok := kv.Get("a")
	if !ok || got != 1 {
		t.Fatalf("Get(a) = (%d, %v), want (1, true)", got, ok)
	}
	got, ok = kv.Get("b")
	if !ok || got != 2 {
		t.Fatalf("Get(b) = (%d, %v), want (2, true)", got, ok)
	}
	if _, ok := kv.Get("c"); ok {
		t.Fatal("Get(c) ok=true, want false")
	}
	if got := kv.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
}

func TestKV_SetOverwrite(t *testing.T) {
	kv := NewKV[string, string]()
	kv.Set("k", "v1")
	kv.Set("k", "v2")

	got, ok := kv.Get("k")
	if !ok || got != "v2" {
		t.Fatalf("Get(k) = (%q, %v), want (\"v2\", true)", got, ok)
	}
	if got := kv.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestKV_Delete(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)

	kv.Delete("a")
	if _, ok := kv.Get("a"); ok {
		t.Fatal("Get(a) after Delete returned ok=true")
	}
	got, ok := kv.Get("b")
	if !ok || got != 2 {
		t.Fatalf("Get(b) = (%d, %v), want (2, true)", got, ok)
	}

	// Delete missing key is a no-op.
	kv.Delete("missing")
	if got := kv.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestKV_DeleteIf(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)

	if kv.DeleteIf("a", 99) {
		t.Fatal("DeleteIf(a, wrong) = true, want false")
	}
	if got, ok := kv.Get("a"); !ok || got != 1 {
		t.Fatalf("Get(a) after failed DeleteIf = (%d, %v), want (1, true)", got, ok)
	}

	if !kv.DeleteIf("a", 1) {
		t.Fatal("DeleteIf(a, 1) = false, want true")
	}
	if _, ok := kv.Get("a"); ok {
		t.Fatal("Get(a) after DeleteIf still present")
	}
	if got, ok := kv.Get("b"); !ok || got != 2 {
		t.Fatalf("Get(b) = (%d, %v), want (2, true)", got, ok)
	}

	if kv.DeleteIf("missing", 0) {
		t.Fatal("DeleteIf(missing) = true, want false")
	}
}

func TestKV_DeleteIf_PointerIdentity(t *testing.T) {
	type handle struct{ id int }

	kv := NewKV[string, *handle]()
	h1 := &handle{id: 1}
	h2 := &handle{id: 1} // equal fields, different pointer
	kv.Set("run", h1)

	if kv.DeleteIf("run", h2) {
		t.Fatal("DeleteIf with different pointer = true, want false")
	}
	if got, ok := kv.Get("run"); !ok || got != h1 {
		t.Fatal("entry should still be h1 after failed DeleteIf")
	}

	if !kv.DeleteIf("run", h1) {
		t.Fatal("DeleteIf with same pointer = false, want true")
	}
	if _, ok := kv.Get("run"); ok {
		t.Fatal("entry should be gone after DeleteIf(h1)")
	}
}

func TestKV_Pop(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 10)

	got, ok := kv.Pop("a")
	if !ok || got != 10 {
		t.Fatalf("Pop(a) = (%d, %v), want (10, true)", got, ok)
	}
	if _, ok := kv.Get("a"); ok {
		t.Fatal("Get(a) after Pop returned ok=true")
	}
	if got := kv.Len(); got != 0 {
		t.Fatalf("Len() after Pop = %d, want 0", got)
	}

	got, ok = kv.Pop("a")
	if ok || got != 0 {
		t.Fatalf("Pop(missing) = (%d, %v), want (0, false)", got, ok)
	}
}

func TestKV_Clear(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)

	kv.Clear()
	if got := kv.Len(); got != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", got)
	}
	if _, ok := kv.Get("a"); ok {
		t.Fatal("Get(a) after Clear returned ok=true")
	}

	// Store remains usable after Clear.
	kv.Set("c", 3)
	got, ok := kv.Get("c")
	if !ok || got != 3 {
		t.Fatalf("Get(c) after Clear+Set = (%d, %v), want (3, true)", got, ok)
	}
}

func TestKV_Keys_Empty(t *testing.T) {
	kv := NewKV[string, int]()
	keys := kv.Keys()
	if keys == nil {
		t.Fatal("Keys() = nil, want empty non-nil slice")
	}
	if len(keys) != 0 {
		t.Fatalf("Keys() len = %d, want 0", len(keys))
	}
}

func TestKV_Keys(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)
	kv.Set("c", 3)

	keys := kv.Keys()
	if len(keys) != 3 {
		t.Fatalf("Keys() len = %d, want 3", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Fatalf("Keys() missing %q; got %v", want, keys)
		}
	}

	// Snapshot: mutating the store after Keys() must not change the returned slice.
	kv.Delete("a")
	kv.Set("d", 4)
	if len(keys) != 3 {
		t.Fatalf("Keys() snapshot mutated: len = %d, want 3", len(keys))
	}
	for _, k := range keys {
		if k == "d" {
			t.Fatal("Keys() snapshot unexpectedly contains key added after Keys()")
		}
	}
}

func TestKV_Keys_AfterDelete(t *testing.T) {
	kv := NewKV[string, int]()
	kv.Set("a", 1)
	kv.Set("b", 2)
	kv.Delete("a")

	keys := kv.Keys()
	if len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("Keys() = %v, want [b]", keys)
	}
}

func TestKV_Concurrent(t *testing.T) {
	kv := NewKV[int, int]()
	const n = 100
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			kv.Set(i, i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, _ = kv.Get(i)
			_ = kv.Len()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			kv.Delete(i / 2)
			_, _ = kv.Pop(i / 3)
		}
	}()
	wg.Wait()

	// Final write + clear to ensure the map is still coherent.
	kv.Set(999, 1)
	if got, ok := kv.Get(999); !ok || got != 1 {
		t.Fatalf("Get(999) = (%d, %v), want (1, true)", got, ok)
	}
	kv.Clear()
	if got := kv.Len(); got != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", got)
	}
}
