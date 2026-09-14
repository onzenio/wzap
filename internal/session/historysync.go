package session

import (
	"sync"
	"time"
)

// HistorySyncMessage is one historical message of the per-instance
// history-sync feed. The fields are best-effort: the feed orients the Import
// plan (order, dedup by MessageID, attribution), which re-parses anything it
// needs with full fidelity.
type HistorySyncMessage struct {
	MessageID string
	ChatJID   string
	SenderJID string
	IsFromMe  bool
	Timestamp time.Time
	Text      string
}

// HistorySyncConversation is one batched chat of the feed.
type HistorySyncConversation struct {
	ChatJID  string
	Name     string
	Messages []HistorySyncMessage
}

// HistorySyncContact is one contact of the feed, from pushnames or inline
// contacts.
type HistorySyncContact struct {
	JID  string
	Name string
}

// HistorySyncChunk is one observed history-sync payload: part of the batches
// plus the contacts riding the same chunk.
type HistorySyncChunk struct {
	SyncType      string
	Progress      uint32
	Conversations []HistorySyncConversation
	Contacts      []HistorySyncContact
}

// HistorySyncSnapshot is the accumulated per-instance feed the Import plan
// consumes: progress and totals, the merged conversation batches and the
// merged contacts, plus whether the sync completed.
type HistorySyncSnapshot struct {
	Chunks        uint64
	Progress      uint32
	SyncType      string
	Total         int
	Conversations []HistorySyncConversation
	Contacts      []HistorySyncContact
	Completed     bool
	UpdatedAt     time.Time
}

// HistorySyncAccumulator merges history-sync chunks of one instance. It is
// safe for concurrent use; each session owns one, so feeds never cross
// instance boundaries. Use it by value only through a pointer and never copy
// it after first use.
type HistorySyncAccumulator struct {
	mu         sync.Mutex
	chunks     uint64
	progress   uint32
	syncType   string
	total      int
	convIdx    map[string]int
	convs      []HistorySyncConversation
	msgIdx     map[string]map[string]struct{}
	contactIdx map[string]int
	contacts   []HistorySyncContact
	completed  bool
	updatedAt  time.Time
}

// Observe merges chunk into the feed: the chunk counter grows, progress keeps
// the max, conversations merge by chat (messages append deduped by id, the
// first non-empty name wins) and contacts merge by JID (the first non-empty
// name wins).
func (a *HistorySyncAccumulator) Observe(chunk HistorySyncChunk) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureInit()

	a.chunks++
	if chunk.Progress > a.progress {
		a.progress = chunk.Progress
	}
	if chunk.SyncType != "" {
		a.syncType = chunk.SyncType
	}
	for _, conv := range chunk.Conversations {
		a.mergeConversation(conv)
	}
	for _, contact := range chunk.Contacts {
		a.mergeContact(contact)
	}
	a.updatedAt = time.Now().UTC()
}

// ObservePreview records the totals announced before the batches arrive;
// the max wins so repeated previews never shrink the expectation.
func (a *HistorySyncAccumulator) ObservePreview(total int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if total > a.total {
		a.total = total
	}
	a.updatedAt = time.Now().UTC()
}

// MarkComplete flags the feed as fully received.
func (a *HistorySyncAccumulator) MarkComplete() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completed = true
	a.updatedAt = time.Now().UTC()
}

// Snapshot returns a copy of the accumulated feed.
func (a *HistorySyncAccumulator) Snapshot() HistorySyncSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	snap := HistorySyncSnapshot{
		Chunks:    a.chunks,
		Progress:  a.progress,
		SyncType:  a.syncType,
		Total:     a.total,
		Completed: a.completed,
		UpdatedAt: a.updatedAt,
	}
	for _, conv := range a.convs {
		dup := HistorySyncConversation{
			ChatJID:  conv.ChatJID,
			Name:     conv.Name,
			Messages: append([]HistorySyncMessage(nil), conv.Messages...),
		}
		snap.Conversations = append(snap.Conversations, dup)
	}
	snap.Contacts = append([]HistorySyncContact(nil), a.contacts...)
	return snap
}

// Reset clears the feed so the next sync starts from zero. Fields are
// cleared one by one: assigning the whole struct would copy the held mutex.
func (a *HistorySyncAccumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chunks = 0
	a.progress = 0
	a.syncType = ""
	a.total = 0
	a.convIdx = nil
	a.convs = nil
	a.msgIdx = nil
	a.contactIdx = nil
	a.contacts = nil
	a.completed = false
	a.updatedAt = time.Time{}
}

// ensureInit lazies the merge indexes; the accumulator is usable as a zero
// value. Callers must hold the mutex.
func (a *HistorySyncAccumulator) ensureInit() {
	if a.convIdx == nil {
		a.convIdx = make(map[string]int)
	}
	if a.msgIdx == nil {
		a.msgIdx = make(map[string]map[string]struct{})
	}
	if a.contactIdx == nil {
		a.contactIdx = make(map[string]int)
	}
}

// mergeConversation folds conv into the accumulated batches. Callers must hold
// the mutex and have initialized the indexes.
func (a *HistorySyncAccumulator) mergeConversation(conv HistorySyncConversation) {
	idx, ok := a.convIdx[conv.ChatJID]
	if !ok {
		idx = len(a.convs)
		a.convIdx[conv.ChatJID] = idx
		a.convs = append(a.convs, HistorySyncConversation{ChatJID: conv.ChatJID, Name: conv.Name})
		a.msgIdx[conv.ChatJID] = make(map[string]struct{})
	}
	stored := &a.convs[idx]
	if stored.Name == "" {
		stored.Name = conv.Name
	}
	seen := a.msgIdx[conv.ChatJID]
	for _, msg := range conv.Messages {
		if msg.MessageID == "" {
			continue
		}
		if _, dup := seen[msg.MessageID]; dup {
			continue
		}
		seen[msg.MessageID] = struct{}{}
		if msg.ChatJID == "" {
			msg.ChatJID = stored.ChatJID
		}
		stored.Messages = append(stored.Messages, msg)
	}
}

// mergeContact folds contact into the accumulated contacts. Callers must hold
// the mutex and have initialized the indexes.
func (a *HistorySyncAccumulator) mergeContact(contact HistorySyncContact) {
	if contact.JID == "" {
		return
	}
	idx, ok := a.contactIdx[contact.JID]
	if !ok {
		idx = len(a.contacts)
		a.contactIdx[contact.JID] = idx
		a.contacts = append(a.contacts, contact)
		return
	}
	if a.contacts[idx].Name == "" {
		a.contacts[idx].Name = contact.Name
	}
}
