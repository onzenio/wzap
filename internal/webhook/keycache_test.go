package webhook

import (
	"testing"

	"github.com/google/uuid"
)

func TestKeyCacheMiss(t *testing.T) {
	cache := NewKeyCache()
	if got, ok := cache.Get(uuid.New()); ok || got != "" {
		t.Errorf("Get on a fresh cache = %q, %v, want %q, false", got, ok, "")
	}
}

func TestKeyCacheStoreAndGet(t *testing.T) {
	cache := NewKeyCache()
	id := uuid.New()
	cache.Store(id, "key-one")

	got, ok := cache.Get(id)
	if !ok || got != "key-one" {
		t.Errorf("Get = %q, %v, want %q, true", got, ok, "key-one")
	}

	// Unknown ids still miss while a sibling is stored.
	if got, ok := cache.Get(uuid.New()); ok || got != "" {
		t.Errorf("Get(unknown) = %q, %v, want %q, false", got, ok, "")
	}
}

func TestKeyCacheStoreOverwrites(t *testing.T) {
	cache := NewKeyCache()
	id := uuid.New()
	cache.Store(id, "key-old")
	// Rotation swaps the credential immediately: the new key wins.
	cache.Store(id, "key-new")

	if got, ok := cache.Get(id); !ok || got != "key-new" {
		t.Errorf("Get after rotation = %q, %v, want %q, true", got, ok, "key-new")
	}
}

func TestKeyCacheClear(t *testing.T) {
	cache := NewKeyCache()
	id := uuid.New()
	cache.Store(id, "key-one")
	cache.Clear(id)

	if got, ok := cache.Get(id); ok || got != "" {
		t.Errorf("Get after Clear = %q, %v, want %q, false", got, ok, "")
	}

	// Clearing a missing id is a no-op, never a panic.
	cache.Clear(uuid.New())
}

func TestKeyCacheIsolatesInstances(t *testing.T) {
	cache := NewKeyCache()
	first, second := uuid.New(), uuid.New()
	cache.Store(first, "key-first")
	cache.Store(second, "key-second")

	if got, _ := cache.Get(first); got != "key-first" {
		t.Errorf("Get(first) = %q, want %q", got, "key-first")
	}
	if got, _ := cache.Get(second); got != "key-second" {
		t.Errorf("Get(second) = %q, want %q", got, "key-second")
	}

	cache.Clear(first)
	if _, ok := cache.Get(first); ok {
		t.Error("Get(first) after Clear still hits, want a miss")
	}
	if got, _ := cache.Get(second); got != "key-second" {
		t.Errorf("Get(second) = %q, want the sibling untouched", got)
	}
}
