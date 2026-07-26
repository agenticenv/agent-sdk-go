package store

import "sync"

// KV is a thread-safe, generic key-value store.
type KV[K comparable, V any] struct {
	mu   sync.RWMutex
	data map[K]V
}

// NewKV initializes and returns a new generic KV store.
func NewKV[K comparable, V any]() *KV[K, V] {
	return &KV[K, V]{
		data: make(map[K]V),
	}
}

// Set adds or updates a key-value pair.
func (s *KV[K, V]) Set(key K, value V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

// Get retrieves a value by key.
func (s *KV[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.data[key]
	return val, ok
}

// Delete removes a key-value pair from the store.
func (s *KV[K, V]) Delete(key K) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Keys returns all keys in the store.
func (s *KV[K, V]) Keys() []K {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]K, 0, len(s.data))
	for key := range s.data {
		keys = append(keys, key)
	}
	return keys
}

// DeleteIf removes key only when the stored value equals expected (same lock).
// Returns true if the key was deleted.
//
// V must be comparable at runtime (e.g. pointers, strings, ints). Comparing
// non-comparable values (slices, maps, funcs) panics — same as Go interface ==.
func (s *KV[K, V]) DeleteIf(key K, expected V) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.data[key]
	if !ok || any(current) != any(expected) {
		return false
	}
	delete(s.data, key)
	return true
}

// Pop atomically retrieves and deletes a key-value pair in a single lock step.
func (s *KV[K, V]) Pop(key K) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	val, ok := s.data[key]
	if ok {
		delete(s.data, key)
	}
	return val, ok
}

// Len returns the current count of items stored.
func (s *KV[K, V]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Clear removes all keys and resets the inner map.
func (s *KV[K, V]) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[K]V)
}
