package conversations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
)

// convFixture is a configurable Chatwoot HTTP fake for conversation tests.
type convFixture struct {
	list     string
	get      map[string]string
	creates  atomic.Int64
	toggles  atomic.Int64
	lastBody map[string]any
	mu       sync.Mutex
	events   []string
}

func (f *convFixture) logEvent(ev string) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}

func (f *convFixture) dumpEvents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *convFixture) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/conversations") && r.Method == http.MethodGet:
			f.logEvent("LIST -> " + f.list)
			_, _ = w.Write([]byte(f.list))
		case strings.HasSuffix(path, "/conversations") && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			f.mu.Lock()
			f.lastBody = body
			f.mu.Unlock()
			n := f.creates.Add(1)
			f.logEvent("CREATE #" + strconv.FormatInt(n, 10))
			_, _ = w.Write([]byte(`{"id":100,"status":"open","inbox_id":42}`))
		case strings.Contains(path, "/conversations/") && strings.HasSuffix(path, "/toggle_status"):
			f.toggles.Add(1)
			_, _ = w.Write([]byte(`{"id":8,"status":"open","inbox_id":42}`))
		case strings.Contains(path, "/conversations/"):
			id := path[strings.LastIndex(path, "/")+1:]
			if resp, ok := f.get[id]; ok {
				f.logEvent("GET " + id + " -> hit")
				_, _ = w.Write([]byte(resp))
				return
			}
			f.logEvent("GET " + id + " -> 404")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		default:
			t.Errorf("unexpected path %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newConvResolver(srv *httptest.Server, cfg model.ChatwootConfig) *Resolver {
	r := New(client.New(srv.URL, "test-token", "1"), cfg, 42, nil)
	r.locks.poll = 5 * time.Millisecond
	return r
}

// TestConversationKeyScopesByInstance pins the string key shape Task 6 relies on.
func TestConversationKeyScopesByInstance(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	if got, want := conversationKey(id, "5511999999999@s.whatsapp.net"), id.String()+"|5511999999999@s.whatsapp.net"; got != want {
		t.Errorf("conversationKey = %q, want %q", got, want)
	}
}

// TestResolveConvergesConcurrentSenders ensures a burst for one sender opens a
// single conversation: the real locker plus double-check collapse 20
// goroutines into one CreateConversation call.
func TestResolveConvergesConcurrentSenders(t *testing.T) {
	f := &convFixture{list: `[]`, get: map[string]string{}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	resolver := newConvResolver(srv, model.ChatwootConfig{})

	const runners = 20
	instanceID := uuid.New()
	ids := make([]int64, runners)
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, err := resolver.Resolve(context.Background(), instanceID, "5511999999999@s.whatsapp.net", 7)
			if err != nil {
				t.Errorf("Resolve = %v, want nil", err)
				return
			}
			ids[n] = id
		}(i)
	}
	wg.Wait()
	for i, id := range ids {
		if id != 100 {
			t.Errorf("ids[%d] = %d, want 100 (single shared conversation)", i, id)
		}
	}
	if got := f.creates.Load(); got != 1 {
		t.Errorf("CreateConversation calls = %d, want 1\nevents:\n%s", got, strings.Join(f.dumpEvents(), "\n"))
	}
}

// TestResolveReusesOpenConversation ensures an open inbox conversation is
// reused and the second call validates through the cache via GetConversation.
func TestResolveReusesOpenConversation(t *testing.T) {
	open := `{"id":5,"status":"open","inbox_id":42}`
	f := &convFixture{list: `[` + open + `]`, get: map[string]string{"5": open}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	resolver := newConvResolver(srv, model.ChatwootConfig{})

	instanceID := uuid.New()
	for i := 0; i < 2; i++ {
		id, err := resolver.Resolve(context.Background(), instanceID, "5511999999999@s.whatsapp.net", 7)
		if err != nil {
			t.Fatalf("Resolve = %v, want nil", err)
		}
		if id != 5 {
			t.Fatalf("id = %d, want 5 (reuse open)", id)
		}
	}
	if got := f.creates.Load(); got != 0 {
		t.Errorf("CreateConversation calls = %d, want 0 (reuse, no create)", got)
	}
}

// TestResolvePendingNeedsFlag ensures a pending conversation is reused only
// when conversation_pending is set, otherwise a new one opens.
func TestResolvePendingNeedsFlag(t *testing.T) {
	pending := `{"id":6,"status":"pending","inbox_id":42}`
	for _, tc := range []struct {
		name   string
		config model.ChatwootConfig
		want   int64
	}{
		{"flag off creates", model.ChatwootConfig{}, 100},
		{"flag on reuses", model.ChatwootConfig{ConversationPending: true}, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &convFixture{list: `[` + pending + `]`, get: map[string]string{"6": pending}}
			srv := httptest.NewServer(f.handler(t))
			defer srv.Close()
			id, err := newConvResolver(srv, tc.config).Resolve(context.Background(), uuid.New(), "5511999999999@s.whatsapp.net", 7)
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}
			if id != tc.want {
				t.Errorf("id = %d, want %d", id, tc.want)
			}
		})
	}
}

// TestResolveReopensResolvedWithFlag ensures a resolved conversation reopens
// only when reopen_conversation is set, otherwise a new one opens.
func TestResolveReopensResolvedWithFlag(t *testing.T) {
	resolved := `{"id":8,"status":"resolved","inbox_id":42}`
	for _, tc := range []struct {
		name    string
		config  model.ChatwootConfig
		want    int64
		toggles int64
	}{
		{"flag off creates", model.ChatwootConfig{}, 100, 0},
		{"flag on reopens", model.ChatwootConfig{ReopenConversation: true}, 8, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &convFixture{list: `[` + resolved + `]`, get: map[string]string{}}
			srv := httptest.NewServer(f.handler(t))
			defer srv.Close()
			id, err := newConvResolver(srv, tc.config).Resolve(context.Background(), uuid.New(), "5511999999999@s.whatsapp.net", 7)
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}
			if id != tc.want {
				t.Errorf("id = %d, want %d", id, tc.want)
			}
			if got := f.toggles.Load(); got != tc.toggles {
				t.Errorf("toggle calls = %d, want %d", got, tc.toggles)
			}
		})
	}
}

// TestResolveCreatesPendingStatusWithFlag ensures creation carries
// status:pending exactly when conversation_pending is set.
func TestResolveCreatesPendingStatusWithFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config model.ChatwootConfig
		want   string
	}{
		{"flag off omits status", model.ChatwootConfig{}, ""},
		{"flag on sends pending", model.ChatwootConfig{ConversationPending: true}, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &convFixture{list: `[]`, get: map[string]string{"100": `{"id":100,"status":"open","inbox_id":42}`}}
			srv := httptest.NewServer(f.handler(t))
			defer srv.Close()
			if _, err := newConvResolver(srv, tc.config).Resolve(context.Background(), uuid.New(), "5511999999999@s.whatsapp.net", 7); err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			status, _ := f.lastBody["status"].(string)
			if status != tc.want {
				t.Errorf("create status = %q, want %q", status, tc.want)
			}
		})
	}
}

// TestResolveDropsStaleCacheEntry ensures a cached id that no longer
// validates (404) is dropped and replaced by a fresh conversation.
func TestResolveDropsStaleCacheEntry(t *testing.T) {
	f := &convFixture{list: `[]`, get: map[string]string{}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	resolver := newConvResolver(srv, model.ChatwootConfig{})

	instanceID := uuid.New()
	resolver.store(conversationKey(instanceID, "5511999999999@s.whatsapp.net"), 999)
	id, err := resolver.Resolve(context.Background(), instanceID, "5511999999999@s.whatsapp.net", 7)
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if id != 100 {
		t.Errorf("id = %d, want 100 (stale cache replaced)", id)
	}
	if got := f.creates.Load(); got != 1 {
		t.Errorf("CreateConversation calls = %d, want 1", got)
	}
}
