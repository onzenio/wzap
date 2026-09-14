package replicalock

import (
	"context"
	"testing"
	"time"

	"wzap/internal/storage/postgres/postgrestest"
)

// RED: segunda réplica não adquire o lock enquanto a primeira segura.
func TestSecondReplicaBlocked(t *testing.T) {
	pool := postgrestest.NewPool(t)
	pool2 := postgrestest.NewPool(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	release, err := TryAcquire(ctx, pool)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}
	defer release(context.Background())

	if _, err := TryAcquire(ctx, pool2); err == nil {
		t.Fatal("second TryAcquire = nil, want ErrReplicaRunning")
	} else if err != ErrReplicaRunning {
		t.Fatalf("second TryAcquire err = %v, want ErrReplicaRunning", err)
	}
}
