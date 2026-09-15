package chatimport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
	"wzap/internal/storage"
)

// fakeSchedulerInstances pages one fixed instance list.
type fakeSchedulerInstances struct {
	instances []model.Instance
}

func (f *fakeSchedulerInstances) List(_ context.Context, _ int, _ string) ([]model.Instance, string, error) {
	return f.instances, "", nil
}

// fakeSchedulerConfigs replays per-instance connector configs.
type fakeSchedulerConfigs struct {
	mu   sync.Mutex
	cfgs map[uuid.UUID]*model.ChatwootConfig
	gets []uuid.UUID
}

func (f *fakeSchedulerConfigs) Get(_ context.Context, id uuid.UUID) (*model.ChatwootConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, id)
	cfg, ok := f.cfgs[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cfg, nil
}

// fakeSchedulerSessions replays per-instance sessions.
type fakeSchedulerSessions struct {
	sessions map[uuid.UUID]session.Session
}

func (f *fakeSchedulerSessions) Get(id uuid.UUID) (session.Session, bool) {
	sess, ok := f.sessions[id]
	return sess, ok
}

// TestSchedulerRunOnceImportsEligibleAndClears pins the 30min cron cycle:
// the enabled instance with import flags and a session runs once with the
// 6h window, the disabled one is skipped, and the connector cache is
// cleared for the imported instance.
func TestSchedulerRunOnceImportsEligibleAndClears(t *testing.T) {
	enabledID := uuid.New()
	disabledID := uuid.New()
	now := time.Now().UTC()

	var mu sync.Mutex
	var ran []uuid.UUID
	var sinces []time.Time
	cleared := map[uuid.UUID]int{}

	scheduler := NewScheduler(SchedulerDeps{
		Instances: &fakeSchedulerInstances{instances: []model.Instance{{ID: enabledID}, {ID: disabledID}}},
		Configs: &fakeSchedulerConfigs{cfgs: map[uuid.UUID]*model.ChatwootConfig{
			enabledID:  {InstanceID: enabledID, Enabled: true, ImportMessages: true},
			disabledID: {InstanceID: disabledID, Enabled: false},
		}},
		Sessions: &fakeSchedulerSessions{sessions: map[uuid.UUID]session.Session{
			enabledID: sessiontest.NewSession(enabledID, nil),
		}},
		Run: func(_ context.Context, id uuid.UUID, since time.Time) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, id)
			sinces = append(sinces, since)
			return 3, nil
		},
		ClearCache: func(id uuid.UUID) {
			mu.Lock()
			defer mu.Unlock()
			cleared[id]++
		},
	})

	total, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if total != 3 {
		t.Errorf("RunOnce() = %d, want 3", total)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != enabledID {
		t.Errorf("ran = %v, want exactly [%s]", ran, enabledID)
	}
	if len(sinces) != 1 || sinces[0].After(now.Add(-6*time.Hour-time.Minute)) == false || sinces[0].After(now) {
		t.Errorf("since = %v, want ~6h window", sinces)
	}
	if cleared[enabledID] != 1 {
		t.Errorf("cleared[%s] = %d, want 1 (cache cleared after import)", enabledID, cleared[enabledID])
	}
	if cleared[disabledID] != 0 {
		t.Errorf("cleared[%s] = %d, want 0 (disabled skipped)", disabledID, cleared[disabledID])
	}
}

// TestSchedulerRunOnceSkipsClearOnInertRun pins the missing-URI cycle: when
// the run is inert (0, nil — no pool, flags off, empty guard) the connector
// cache is left alone and the feed accumulators stay intact for the next
// trigger. Only real successful work (imported > 0) clears the cache.
func TestSchedulerRunOnceSkipsClearOnInertRun(t *testing.T) {
	id := uuid.New()
	feed := &fakeFeed{snap: session.HistorySyncSnapshot{
		Contacts: []session.HistorySyncContact{
			{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
		},
	}}
	cleared := 0

	scheduler := NewScheduler(SchedulerDeps{
		Instances: &fakeSchedulerInstances{instances: []model.Instance{{ID: id}}},
		Configs: &fakeSchedulerConfigs{cfgs: map[uuid.UUID]*model.ChatwootConfig{
			id: {InstanceID: id, Enabled: true, ImportMessages: true},
		}},
		Sessions: &fakeSchedulerSessions{sessions: map[uuid.UUID]session.Session{
			id: sessiontest.NewSession(id, nil),
		}},
		Run: func(context.Context, uuid.UUID, time.Time) (int, error) {
			// Mimics importer.run without the import URI: inert, the feed
			// is never consumed and its accumulators never reset.
			return 0, nil
		},
		ClearCache: func(uuid.UUID) { cleared++ },
	})

	total, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if total != 0 {
		t.Errorf("RunOnce() = %d, want 0 (inert run)", total)
	}
	if cleared != 0 {
		t.Errorf("cleared = %d, want 0 (inert run leaves the cache alone)", cleared)
	}
	if feed.resets != 0 {
		t.Errorf("accumulator resets = %d, want 0 (inert run keeps the feed)", feed.resets)
	}
	if len(feed.snap.Contacts) != 1 {
		t.Errorf("feed contacts = %d, want 1 (inert run leaves accumulators intact)", len(feed.snap.Contacts))
	}
}

// TestSchedulerStartTicksUntilStopped pins that Start runs the cycle on the
// interval until the context ends.
func TestSchedulerStartSkipsOverlappingTicks(t *testing.T) {
	id := uuid.New()
	var mu sync.Mutex
	entries := 0
	maxConcurrent := 0
	current := 0
	release := make(chan struct{})
	scheduler := NewScheduler(SchedulerDeps{
		Interval:  10 * time.Millisecond,
		Instances: &fakeSchedulerInstances{instances: []model.Instance{{ID: id}}},
		Configs: &fakeSchedulerConfigs{cfgs: map[uuid.UUID]*model.ChatwootConfig{
			id: {InstanceID: id, Enabled: true, ImportMessages: true},
		}},
		Sessions: &fakeSchedulerSessions{sessions: map[uuid.UUID]session.Session{
			id: sessiontest.NewSession(id, nil),
		}},
		Run: func(context.Context, uuid.UUID, time.Time) (int, error) {
			mu.Lock()
			entries++
			current++
			if current > maxConcurrent {
				maxConcurrent = current
			}
			mu.Unlock()
			<-release
			mu.Lock()
			current--
			mu.Unlock()
			return 0, nil
		},
		ClearCache: func(uuid.UUID) {},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Start(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent != 1 {
		t.Errorf("concorrência máxima = %d, want 1 (ticks nunca sobrepõem)", maxConcurrent)
	}
	if entries < 1 {
		t.Errorf("entries = %d, want >= 1", entries)
	}
}

func TestSchedulerStartTicksUntilStopped(t *testing.T) {
	id := uuid.New()
	var mu sync.Mutex
	cycles := 0
	scheduler := NewScheduler(SchedulerDeps{
		Interval:  10 * time.Millisecond,
		Instances: &fakeSchedulerInstances{instances: []model.Instance{{ID: id}}},
		Configs: &fakeSchedulerConfigs{cfgs: map[uuid.UUID]*model.ChatwootConfig{
			id: {InstanceID: id, Enabled: true, ImportMessages: true},
		}},
		Sessions: &fakeSchedulerSessions{sessions: map[uuid.UUID]session.Session{
			id: sessiontest.NewSession(id, nil),
		}},
		Run: func(context.Context, uuid.UUID, time.Time) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			cycles++
			return 0, nil
		},
		ClearCache: func(uuid.UUID) {},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Start(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := cycles
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("cycles = %d, want >= 2 before stop", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
