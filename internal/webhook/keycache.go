package webhook

import (
	"sync"

	"github.com/google/uuid"
)

// KeyCache is the process-local store of instance API key plaintexts. The
// database holds only hashes (see auth.MintAPIKey), so webhook delivery —
// which must send the vigente key in the apikey header — keeps the plaintext
// here, in memory only. It is never logged, persisted, or embedded anywhere
// else: only the single delivery header carries it out of the process.
//
// Restart-cold limitation: the cache starts empty on every boot, so deliveries
// for pre-existing instances fail recorded (the "sem key" path) until the next
// rotation repopulates it. Task 7.1 documents this for operators.
type KeyCache struct {
	mu   sync.RWMutex
	keys map[uuid.UUID]string
}

// Keys is the shared process-local cache. Handlers populate it on create and
// rotate, evict on revoke and instance delete; the delivery worker (Task 4.3)
// reads it at dispatch time. There is a single replica by design, so no
// cross-process state is needed.
var Keys = NewKeyCache()

// NewKeyCache returns an empty KeyCache ready to use. The zero value is valid
// too: Store lazily initializes the map.
func NewKeyCache() *KeyCache {
	return &KeyCache{keys: make(map[uuid.UUID]string)}
}

// Store records the plaintext key of instanceID, overwriting any prior entry
// so a rotation swaps the credential immediately.
func (c *KeyCache) Store(instanceID uuid.UUID, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil {
		c.keys = make(map[uuid.UUID]string)
	}
	c.keys[instanceID] = key
}

// Get returns the cached plaintext key of instanceID, or ("", false) on a
// miss. A miss means delivery must take the "sem key" failure path.
func (c *KeyCache) Get(instanceID uuid.UUID) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.keys[instanceID]
	return key, ok
}

// Clear evicts the cached key of instanceID. Clearing a missing id is a
// no-op.
func (c *KeyCache) Clear(instanceID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.keys, instanceID)
}
