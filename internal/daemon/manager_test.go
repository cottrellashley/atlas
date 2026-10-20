package daemon

import (
	"context"
	"errors"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/store"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDriver struct {
	run func(context.Context, harness.RunRequest, harness.Emit, <-chan harness.Control) (harness.RunResult, error)
}

func TestFailedSendPersistenceDoesNotLeavePhantomRun(t *testing.T) {
	m := manager(t, func(context.Context, harness.RunRequest, harness.Emit, <-chan harness.Control) (harness.RunResult, error) {
		return harness.RunResult{}, nil
	})
	native := harness.NativeSession{CWD: t.TempDir(), Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]string{"id": "native"})}}
	r, e := m.Import("pi", native)
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := m.store.Dir(r.ID)
	path := filepath.Join(dir, "record.json")
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Send(r.ID, "prompt"); e == nil {
		t.Fatal("expected save failure")
	}
	got, _ := m.Get(r.ID)
	if got.State == "running" || got.State == "waiting_permission" {
		t.Fatal("phantom running state")
	}
}
func TestDurableTerminalReplayClearsPermissions(t *testing.T) {
	home := t.TempDir()
	s := store.Store{Home: home}
	id := store.ID()
	r := store.Record{SchemaVersion: 1, ID: id, State: "waiting_permission", Seq: 1, Permissions: []harness.Permission{{ID: "p"}}}
	if e := s.Save(r); e != nil {
		t.Fatal(e)
	}
	if e := s.Append(id, harness.Event{Seq: 2, Type: "turn.completed"}); e != nil {
		t.Fatal(e)
	}
	m, e := NewManager(home, func(harness.Config) (harness.Driver, error) { return fakeDriver{}, nil })
	if e != nil {
		t.Fatal(e)
	}
	got, _ := m.Get(id)
	if got.State != "idle" || len(got.Permissions) > 0 || got.Seq != 2 {
		t.Fatal(got)
	}
}

func TestFailedEventAppendDoesNotAdvanceCursor(t *testing.T) {
	m := manager(t, func(context.Context, harness.RunRequest, harness.Emit, <-chan harness.Control) (harness.RunResult, error) {
		return harness.RunResult{}, nil
	})
	r, e := m.Import("pi", harness.NativeSession{CWD: t.TempDir(), Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]string{"id": "native"})}})
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := m.store.Dir(r.ID)
	if e = os.Mkdir(filepath.Join(dir, "events.jsonl"), 0700); e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	e = m.event(m.records[r.ID], harness.Event{Type: "text", Text: "lost"})
	got := clone(m.records[r.ID])
	m.mu.Unlock()
	if e == nil || got.Seq != r.Seq {
		t.Fatal("failed append consumed a log cursor", e, got.Seq)
	}
}

func (f fakeDriver) Catalog(context.Context, string) (harness.Catalog, error) {
	return harness.Catalog{}, nil
}
func (f fakeDriver) Discover(context.Context, string, int) ([]harness.NativeSession, error) {
	return nil, nil
}
func (f fakeDriver) Run(c context.Context, r harness.RunRequest, e harness.Emit, controls <-chan harness.Control) (harness.RunResult, error) {
	return f.run(c, r, e, controls)
}
func manager(t *testing.T, run func(context.Context, harness.RunRequest, harness.Emit, <-chan harness.Control) (harness.RunResult, error)) *Manager {
	t.Helper()
	m, e := NewManager(t.TempDir(), func(harness.Config) (harness.Driver, error) { return fakeDriver{run}, nil })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}
func settled(t *testing.T, m *Manager, id string) store.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, e := m.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if r.State != "running" && r.State != "waiting_permission" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("turn did not settle")
	return store.Record{}
}
func TestPersistenceOrderingAndResume(t *testing.T) {
	var turns atomic.Int32
	handle := &harness.Handle{Version: 1, Data: harness.JSON(map[string]string{"sessionId": "native"})}
	m := manager(t, func(ctx context.Context, req harness.RunRequest, emit harness.Emit, c <-chan harness.Control) (harness.RunResult, error) {
		if turns.Add(1) == 2 && canonical(req.Resume) != canonical(handle) {
			return harness.RunResult{}, errors.New("missing resume")
		}
		if e := emit(harness.Event{Type: "persistence", Handle: handle}); e != nil {
			return harness.RunResult{}, e
		}
		if e := emit(harness.Event{Type: "text", Text: "hello"}); e != nil {
			return harness.RunResult{}, e
		}
		return harness.RunResult{Handle: handle, Model: "vendor/model"}, nil
	})
	r, e := m.Run(RunInput{Harness: "pi", CWD: t.TempDir(), Prompt: "first"})
	if e != nil {
		t.Fatal(e)
	}
	r = settled(t, m, r.ID)
	if r.Resume == nil || r.EffectiveModel != "vendor/model" {
		t.Fatal(r)
	}
	if _, e = m.Send(r.ID, "second"); e != nil {
		t.Fatal(e)
	}
	r = settled(t, m, r.ID)
	if r.State != "idle" {
		t.Fatal(r)
	}
	events, e := m.Events(r.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatal("unordered sequence")
		}
	}
	loaded, e := m.store.Load(r.ID)
	if e != nil || loaded.Seq != r.Seq {
		t.Fatal(e)
	}
}
func TestConcurrentSendAndStopSettlement(t *testing.T) {
	exited := make(chan struct{})
	entered := make(chan struct{})
	m := manager(t, func(ctx context.Context, r harness.RunRequest, e harness.Emit, c <-chan harness.Control) (harness.RunResult, error) {
		close(entered)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		close(exited)
		return harness.RunResult{}, ctx.Err()
	})
	r, e := m.Run(RunInput{Harness: "pi", CWD: t.TempDir(), Prompt: "wait"})
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	if _, e = m.Send(r.ID, "second"); e == nil {
		t.Fatal("allowed concurrent turn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = m.Stop(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	select {
	case <-exited:
	default:
		t.Fatal("stop returned before driver cleanup")
	}
	r, _ = m.Get(r.ID)
	if r.State != "stopped" {
		t.Fatal(r.State)
	}
}
func TestPermissionRemainsUntilNativeAcknowledgment(t *testing.T) {
	emitted := make(chan struct{})
	responded := make(chan struct{})
	release := make(chan struct{})
	m := manager(t, func(ctx context.Context, r harness.RunRequest, emit harness.Emit, controls <-chan harness.Control) (harness.RunResult, error) {
		_ = emit(harness.Event{Type: "permission", Permission: &harness.Permission{ID: "p1", Tool: "test"}})
		close(emitted)
		select {
		case <-controls:
			close(responded)
		case <-ctx.Done():
			return harness.RunResult{}, ctx.Err()
		}
		select {
		case <-release:
		case <-ctx.Done():
			return harness.RunResult{}, ctx.Err()
		}
		_ = emit(harness.Event{Type: "permission_resolved", Data: harness.JSON(map[string]string{"id": "p1"})})
		return harness.RunResult{}, nil
	})
	r, e := m.Run(RunInput{Harness: "claude", CWD: t.TempDir(), Prompt: "request"})
	if e != nil {
		t.Fatal(e)
	}
	<-emitted
	if e = m.Permission(r.ID, harness.Control{ID: "p1", Allow: true}); e != nil {
		t.Fatal(e)
	}
	<-responded
	r, _ = m.Get(r.ID)
	if len(r.Permissions) != 1 || r.State != "waiting_permission" {
		t.Fatal("approval was reported before acknowledgment")
	}
	close(release)
	r = settled(t, m, r.ID)
	if len(r.Permissions) != 0 {
		t.Fatal("permission did not resolve")
	}
}
func TestImportIdempotenceAndCrashRecovery(t *testing.T) {
	home := t.TempDir()
	f := func(harness.Config) (harness.Driver, error) { return fakeDriver{}, nil }
	m, e := NewManager(home, f)
	if e != nil {
		t.Fatal(e)
	}
	native := harness.NativeSession{ID: "native", CWD: t.TempDir(), Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]string{"sessionId": "native"})}}
	a, e := m.Import("pi", native)
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.Import("pi", native)
	if e != nil || a.ID != b.ID {
		t.Fatal("import not deduplicated")
	}
	a.State = "running"
	if e = m.store.Save(a); e != nil {
		t.Fatal(e)
	}
	m2, e := NewManager(home, f)
	if e != nil {
		t.Fatal(e)
	}
	r, _ := m2.Get(a.ID)
	if r.State != "interrupted" || r.Resume == nil {
		t.Fatal(r)
	}
}
