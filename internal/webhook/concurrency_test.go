package webhook

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RED: worker deve capar deliveries concorrentes com semáforo.
func TestWorkerCapsConcurrency(t *testing.T) {
	var current atomic.Int32
	var maxSeen atomic.Int32
	release := make(chan struct{})
	deliver := func(ctx context.Context, url, key string, payload []byte) error {
		n := current.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		current.Add(-1)
		return nil
	}
	inst := webhookTestInstance("https://hooks.example.com/wzap")
	keys := NewKeyCache()
	keys.Store(inst.ID, "k")
	worker := NewWorker(newStubLoader(inst), keys, deliver, 16<<20, nil)
	worker.Sleep = func(context.Context, time.Duration) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()

	for i := 0; i < 64; i++ {
		env := testEnvelope(t, "message", inst.ID)
		_ = env
		if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", testEnvelope(t, "message", inst.ID)); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
	// Dá tempo dos jobs ocuparem o semáforo.
	time.Sleep(300 * time.Millisecond)
	close(release)
	// Espera esvaziar.
	deadline := time.Now().Add(5 * time.Second)
	for current.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker não parou")
	}
	_ = uuid.New
	if got := maxSeen.Load(); got > int32(MaxInflight) {
		t.Errorf("concorrência máxima = %d, want <= %d (semáforo)", got, MaxInflight)
	}
	if got := maxSeen.Load(); got <= 1 {
		t.Errorf("concorrência máxima = %d, want >1 (ainda paralelo, só capado)", got)
	}
}
