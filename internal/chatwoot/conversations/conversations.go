// Package conversations resolves a WhatsApp sender to a Chatwoot conversation
// in the instance inbox, safe under bursts: a ~30min cache validated through
// GetConversation plus a per-sender lock with double-check converges
// concurrent senders onto one conversation instead of duplicating it.
package conversations

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
)

// conversationCacheTTL is how long a resolved conversation id is reused
// before revalidation.
const conversationCacheTTL = 30 * time.Minute

// cachedConversation is a resolved conversation id with its expiry.
type cachedConversation struct {
	id        int64
	expiresAt time.Time
}

// Resolver finds or opens Chatwoot conversations through client.
type Resolver struct {
	client  *client.Client
	inboxID int64
	pending bool
	reopen  bool
	log     *slog.Logger

	locks *locker
	mu    sync.Mutex
	cache map[string]cachedConversation
	now   func() time.Time
}

// New builds a Resolver for one inbox. Pending mirrors
// model.ChatwootConfig.ConversationPending (reuse and create pending
// conversations) and reopen mirrors ReopenConversation. A nil log discards
// output.
func New(c *client.Client, cfg model.ChatwootConfig, inboxID int64, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Resolver{
		client:  c,
		inboxID: inboxID,
		pending: cfg.ConversationPending,
		reopen:  cfg.ReopenConversation,
		log:     log,
		locks:   newLocker(),
		cache:   make(map[string]cachedConversation),
		now:     time.Now,
	}
}

// Resolve returns the conversation id for remoteJID, reusing the cached,
// open (or pending when configured) conversation of the inbox, reopening a
// resolved one when configured, or creating a new conversation otherwise.
// Concurrent callers for the same sender converge on one conversation.
func (r *Resolver) Resolve(ctx context.Context, instanceID uuid.UUID, remoteJID string, contactID int64) (int64, error) {
	key := conversationKey(instanceID, remoteJID)
	if id, ok := r.checkCache(ctx, key); ok {
		return id, nil
	}
	release, err := r.locks.acquire(ctx, key)
	if err != nil {
		return 0, err
	}
	defer release()
	// Double-check: a contender may have resolved while this caller waited.
	if id, ok := r.cached(key); ok {
		return id, nil
	}
	conversations, err := r.client.ListContactConversations(ctx, contactID)
	if err != nil {
		return 0, err
	}
	if id, ok, err := r.reuse(ctx, conversations); err != nil {
		return 0, err
	} else if ok {
		r.store(key, id)
		return id, nil
	}
	created, err := r.client.CreateConversation(ctx, client.CreateConversationRequest{
		SourceID:  remoteJID,
		InboxID:   r.inboxID,
		ContactID: contactID,
		Status:    r.createStatus(),
	})
	if err != nil {
		r.log.Warn("conversation creation failed", "remote_jid", remoteJID, "error", err)
		return 0, err
	}
	r.store(key, created.ID)
	return created.ID, nil
}

// checkCache returns the cached conversation when fresh and validated through
// GetConversation as reusable. Anything else drops the entry for the locked
// path to re-list.
func (r *Resolver) checkCache(ctx context.Context, key string) (int64, bool) {
	id, ok := r.cached(key)
	if !ok {
		return 0, false
	}
	conversation, err := r.client.GetConversation(ctx, id)
	if err != nil {
		r.log.Warn("cached conversation validation failed", "conversation_id", id, "error", err)
		r.drop(key)
		return 0, false
	}
	if conversation == nil || conversation.InboxID != r.inboxID {
		r.drop(key)
		return 0, false
	}
	switch conversation.Status {
	case "open":
		r.store(key, id)
		return id, true
	case "pending":
		if r.pending {
			r.store(key, id)
			return id, true
		}
	}
	r.drop(key)
	return 0, false
}

// reuse picks a reusable conversation from the contact listing: open first,
// pending when configured, otherwise a resolved one to reopen when
// configured. Newest (largest id) wins within a class.
func (r *Resolver) reuse(ctx context.Context, conversations []client.Conversation) (int64, bool, error) {
	var open, pending, resolved int64
	for _, conversation := range conversations {
		if conversation.InboxID != r.inboxID {
			continue
		}
		switch conversation.Status {
		case "open":
			open = maxID(open, conversation.ID)
		case "pending":
			pending = maxID(pending, conversation.ID)
		case "resolved":
			resolved = maxID(resolved, conversation.ID)
		}
	}
	switch {
	case open != 0:
		return open, true, nil
	case pending != 0 && r.pending:
		return pending, true, nil
	case resolved != 0 && r.reopen:
		reopened, err := r.client.ToggleConversationStatus(ctx, resolved, "open")
		if err != nil {
			r.log.Warn("conversation reopen failed", "conversation_id", resolved, "error", err)
			return 0, false, err
		}
		return reopened.ID, true, nil
	default:
		return 0, false, nil
	}
}

// createStatus renders the creation status: pending when configured, empty
// (server default open) otherwise.
func (r *Resolver) createStatus() string {
	if r.pending {
		return "pending"
	}
	return ""
}

// conversationKey scopes a sender lock/cache entry to its instance.
func conversationKey(instanceID uuid.UUID, remoteJID string) string {
	return instanceID.String() + "|" + remoteJID
}

// cached returns the fresh cache entry for key.
func (r *Resolver) cached(key string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok || !r.now().Before(entry.expiresAt) {
		return 0, false
	}
	return entry.id, true
}

// store caches id for key for the cache TTL.
func (r *Resolver) store(key string, id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = cachedConversation{id: id, expiresAt: r.now().Add(conversationCacheTTL)}
}

// drop forgets the cache entry for key.
func (r *Resolver) drop(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, key)
}

// Clear drops every cached conversation of instanceID, so the clearcache
// operational command forces fresh resolution on the next message.
func (r *Resolver) Clear(instanceID uuid.UUID) {
	prefix := instanceID.String() + "|"
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.cache {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(r.cache, key)
		}
	}
}

// maxID returns the larger conversation id, treating 0 as absent.
func maxID(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}
