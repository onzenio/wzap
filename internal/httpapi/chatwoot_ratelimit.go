package httpapi

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// ChatwootRateLimiter é um fixed-window por instância para o webhook aberto:
// limita o POST /chatwoot/webhook/{id} a limit requisições por window,
// respondendo 429 rate_limited no estouro. Protege o Enqueue/Download contra
// flood sem credencial; o caminho autenticado não é limitado aqui.
type ChatwootRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[uuid.UUID]*rateWindow
}

type rateWindow struct {
	count int
	reset time.Time
}

// NewChatwootRateLimiter constrói o limiter. limit<=0 desliga (sempre
// permite); window<=0 usa 1 minuto.
func NewChatwootRateLimiter(limit int, window time.Duration) *ChatwootRateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &ChatwootRateLimiter{limit: limit, window: window, hits: make(map[uuid.UUID]*rateWindow)}
}

// Allow consome 1 do orçamento da instância e reporta se a requisição pode
// prosseguir.
func (l *ChatwootRateLimiter) Allow(id uuid.UUID) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[id]
	if !ok || !now.Before(w.reset) {
		l.hits[id] = &rateWindow{count: 1, reset: now.Add(l.window)}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}
