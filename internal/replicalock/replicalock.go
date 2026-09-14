// Package replicalock impede duas réplicas do wzap com o mesmo banco:
// locks de instância são process-local e o runtime suportado é uma réplica,
// então o serve() adquire este advisory lock do Postgres no boot e aborta
// com ErrReplicaRunning se outro processo já o segura.
package replicalock

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LockKey é a chave fixa do advisory lock do serve(). Sessões pg_try_advisory
// são por banco, então qualquer segunda réplica contra o mesmo DATABASE_URL
// falha no boot.
const LockKey int64 = 0x777A6170

// ErrReplicaRunning reporta que outro processo já segura o lock: só uma
// réplica por banco.
var ErrReplicaRunning = errors.New("wzap: another replica already holds the database lock")

// TryAcquire segura o advisory lock numa conexão dedicada do pool até
// release. O chamador mantém release até o shutdown (serve() adquire após
// Connect e solta no fim).
func TryAcquire(ctx context.Context, pool *pgxpool.Pool) (func(context.Context), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("replicalock: acquire connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LockKey).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("replicalock: try lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrReplicaRunning
	}
	released := false
	return func(ctx context.Context) {
		if released {
			return
		}
		released = true
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", LockKey)
		conn.Release()
	}, nil
}
