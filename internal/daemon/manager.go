package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/store"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Factory func(harness.Config) (harness.Driver, error)
type active struct {
	cancel   context.CancelFunc
	controls chan harness.Control
	done     chan struct{}
}
type Manager struct {
	mu      sync.Mutex
	store   store.Store
	records map[string]*store.Record
	active  map[string]*active
	factory Factory
	closing bool
}
type RunInput struct {
	Harness        string `json:"harness"`
	CWD            string `json:"cwd"`
	Agent          string `json:"agent"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	NativeAgent    string `json:"nativeAgent"`
	PermissionMode string `json:"permissionMode"`
	TrustProject   bool   `json:"trustProject"`
}

func NewManager(home string, f Factory) (*Manager, error) {
	m := &Manager{store: store.Store{Home: home}, records: map[string]*store.Record{}, active: map[string]*active{}, factory: f}
	all, e := m.store.List()
	if e != nil {
		return nil, e
	}
	for i := range all {
		r := all[i]
		events, err := m.store.RecoverEvents(r.ID)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			if ev.Seq <= r.Seq {
				continue
			}
			r.Seq = ev.Seq
			r.UpdatedAt = ev.Time
			r.TurnID = ev.TurnID
			if ev.Handle != nil {
				r.Resume = ev.Handle
			}
			if ev.Model != "" {
				r.EffectiveModel = ev.Model
			}
			switch ev.Type {
			case "turn.started":
				r.State = "running"
				r.Error = ""
			case "turn.completed":
				r.State = "idle"
				r.Error = ""
			case "turn.failed":
				r.State = "failed"
				r.Error = ev.Text
			case "turn.stopped":
				r.State = "stopped"
				r.Error = ""
			case "turn.interrupted":
				r.State = "interrupted"
				r.Error = ev.Text
			}
		}
		// No runtime survives recovery; terminal log entries may have been
		// synced before a snapshot that still listed pending permissions.
		r.Permissions = nil
		if r.State == "running" || r.State == "waiting_permission" {
			r.State = "interrupted"
			r.Error = "Atlas daemon stopped before this turn completed; inspect logs before resuming"
			r.Permissions = nil
			r.UpdatedAt = time.Now().UTC()
			r.Seq++
			ev := harness.Event{Seq: r.Seq, Time: r.UpdatedAt, SessionID: r.ID, TurnID: r.TurnID, Type: "turn.interrupted", Text: r.Error}
			if e = m.store.Append(r.ID, ev); e != nil {
				return nil, e
			}
			if e = m.store.Save(r); e != nil {
				return nil, e
			}
		}
		if e = m.store.Save(r); e != nil {
			return nil, e
		}
		m.records[r.ID] = &r
	}
	return m, nil
}
func clone(r *store.Record) store.Record {
	b, _ := json.Marshal(r)
	var v store.Record
	_ = json.Unmarshal(b, &v)
	return v
}
func (m *Manager) Get(id string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return store.Record{}, fmt.Errorf("unknown session %s", id)
	}
	return clone(r), nil
}
func (m *Manager) List() []store.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Record{}
	for _, r := range m.records {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}
func (m *Manager) Events(id string, after int64) ([]harness.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	return m.store.Events(id, after, 1000)
}
func (m *Manager) resolve(id string) (harness.Config, error) {
	c, e := config.Load(m.store.Home)
	if e != nil {
		return harness.Config{}, e
	}
	return c.Resolve(id)
}
func (m *Manager) Run(in RunInput) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return store.Record{}, errors.New("daemon is shutting down")
	}
	cfg, e := m.resolve(in.Harness)
	if e != nil {
		return store.Record{}, e
	}
	cwd, e := filepath.Abs(in.CWD)
	if e != nil {
		return store.Record{}, e
	}
	cwd, e = filepath.EvalSymlinks(cwd)
	if e != nil {
		return store.Record{}, e
	}
	info, e := os.Stat(cwd)
	if e != nil || !info.IsDir() {
		return store.Record{}, errors.New("cwd must be an existing directory")
	}
	now := time.Now().UTC()
	title := strings.TrimSpace(in.Prompt)
	if len(title) > 100 {
		title = title[:100]
	}
	r := &store.Record{SchemaVersion: 1, ID: store.ID(), Harness: in.Harness, Adapter: cfg.Adapter, CWD: cwd, Agent: in.Agent, Title: title, State: "idle", Model: in.Model, Thinking: in.Thinking, NativeAgent: in.NativeAgent, PermissionMode: in.PermissionMode, TrustProject: in.TrustProject, CreatedAt: now, UpdatedAt: now}
	if r.Model == "" {
		r.Model = cfg.Model
	}
	m.records[r.ID] = r
	if e = m.start(r, in.Prompt, cfg); e != nil {
		delete(m.records, r.ID)
		return store.Record{}, e
	}
	return clone(r), nil
}
func (m *Manager) Send(id, prompt string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return store.Record{}, fmt.Errorf("unknown session %s", id)
	}
	cfg, e := m.resolve(r.Harness)
	if e != nil {
		return store.Record{}, e
	}
	if cfg.Adapter != r.Adapter {
		return store.Record{}, errors.New("configured harness adapter changed since session creation")
	}
	if r.Resume == nil && r.TurnID != "" {
		return store.Record{}, errors.New("session has no native resume handle; create a new session")
	}
	if e = m.start(r, prompt, cfg); e != nil {
		return store.Record{}, e
	}
	return clone(r), nil
}
func (m *Manager) event(r *store.Record, e harness.Event) error {
	next := clone(r)
	target := r
	r = &next
	r.Seq++
	r.UpdatedAt = time.Now().UTC()
	e.Seq = r.Seq
	e.Time = r.UpdatedAt
	e.SessionID = r.ID
	e.TurnID = r.TurnID
	if e.Handle != nil {
		r.Resume = e.Handle
	}
	if e.Model != "" {
		r.EffectiveModel = e.Model
	}
	if e.Permission != nil && e.Type == "permission" {
		r.Permissions = append(r.Permissions, *e.Permission)
		r.State = "waiting_permission"
	}
	if e.Type == "permission_resolved" {
		var resolved struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(e.Data, &resolved)
		for i, p := range r.Permissions {
			if p.ID == resolved.ID {
				r.Permissions = append(r.Permissions[:i], r.Permissions[i+1:]...)
				break
			}
		}
		if len(r.Permissions) == 0 {
			r.State = "running"
		}
	}
	if err := m.store.Append(r.ID, e); err != nil {
		return err
	}
	*target = next
	return m.store.Save(*target)
}
func (m *Manager) start(r *store.Record, prompt string, cfg harness.Config) error {
	if m.closing {
		return errors.New("daemon is shutting down")
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("prompt is required")
	}
	if _, ok := m.active[r.ID]; ok {
		return errors.New("session is already running; stop or wait before sending another prompt")
	}
	if r.Archived {
		return errors.New("session is archived; unarchive it before sending")
	}
	if r.TrustProject {
		if cfg.Adapter != "pi" {
			return errors.New("--trust-project is only supported by Pi")
		}
		if cfg.Options == nil {
			cfg.Options = map[string]json.RawMessage{}
		}
		cfg.Options["approve"] = json.RawMessage("true")
	}
	// The managed record owns requested model overrides after creation. Clearing
	// one must not reapply a later host default on resume.
	cfg.Model = ""
	driver, e := m.factory(cfg)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &active{cancel: cancel, controls: make(chan harness.Control, 16), done: make(chan struct{})}
	previous := clone(r)
	r.TurnID = store.ID()
	r.State = "running"
	r.Error = ""
	r.Permissions = nil
	if e = m.store.Save(*r); e != nil {
		cancel()
		*r = previous
		return e
	}
	if e = m.event(r, harness.Event{Type: "turn.started", Text: prompt}); e != nil {
		cancel()
		r.State = "failed"
		r.Error = "turn was not launched: " + e.Error()
		r.Permissions = nil
		_ = m.store.Save(*r)
		return e
	}
	m.active[r.ID] = a
	dir, _ := m.store.Dir(r.ID)
	req := harness.RunRequest{SessionID: r.ID, CWD: r.CWD, Prompt: prompt, Model: r.Model, Thinking: r.Thinking, NativeAgent: r.NativeAgent, PermissionMode: r.PermissionMode, Resume: r.Resume, StateDir: dir}
	go func() {
		defer close(a.done)
		defer cancel()
		result, runErr := driver.Run(ctx, req, func(ev harness.Event) error { m.mu.Lock(); defer m.mu.Unlock(); return m.event(r, ev) }, a.controls)
		m.mu.Lock()
		defer m.mu.Unlock()
		if result.Handle != nil {
			r.Resume = result.Handle
		}
		if result.Model != "" {
			r.EffectiveModel = result.Model
		}
		r.Permissions = nil
		kind := "turn.completed"
		r.State = "idle"
		if runErr != nil {
			r.State = "failed"
			r.Error = runErr.Error()
			kind = "turn.failed"
		}
		if ctx.Err() != nil {
			r.State = "stopped"
			r.Error = ""
			kind = "turn.stopped"
		}
		if e := m.event(r, harness.Event{Type: kind, Text: r.Error}); e != nil {
			r.State = "failed"
			r.Error = "persist terminal state: " + e.Error()
			_ = m.store.Save(*r)
		}
		delete(m.active, r.ID)
	}()
	return nil
}
func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	a, ok := m.active[id]
	_, exists := m.records[id]
	if ok {
		a.cancel()
	}
	m.mu.Unlock()
	if !exists {
		return fmt.Errorf("unknown session %s", id)
	}
	if !ok {
		return nil
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cancellation has not settled: %w", ctx.Err())
	}
}
func (m *Manager) Permission(id string, c harness.Control) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return errors.New("unknown session")
	}
	a, ok := m.active[id]
	if !ok {
		return errors.New("session is not running")
	}
	index := -1
	for i, p := range r.Permissions {
		if p.ID == c.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("unknown pending permission %s", c.ID)
	}
	c.Type = "permission"
	select {
	case a.controls <- c:
	default:
		return errors.New("control queue is full")
	}
	return m.event(r, harness.Event{Type: "permission.responded", Data: harness.JSON(c)})
}
func (m *Manager) Configure(id string, patch map[string]*string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return store.Record{}, errors.New("unknown session")
	}
	if _, ok = m.active[id]; ok {
		return store.Record{}, errors.New("configuration changes apply between turns; wait or stop first")
	}
	next := clone(r)
	for k, v := range patch {
		value := ""
		if v != nil {
			value = *v
		}
		switch k {
		case "model":
			next.Model = value
		case "thinking":
			next.Thinking = value
		case "nativeAgent":
			next.NativeAgent = value
		case "permissionMode":
			next.PermissionMode = value
		default:
			return store.Record{}, fmt.Errorf("unknown setting %s", k)
		}
	}
	next.UpdatedAt = time.Now().UTC()
	if e := m.store.Save(next); e != nil {
		return store.Record{}, e
	}
	*r = next
	return clone(r), nil
}
func (m *Manager) Archive(id string, value bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return errors.New("unknown session")
	}
	if _, ok = m.active[id]; ok {
		return errors.New("stop session before archiving")
	}
	r.Archived = value
	r.UpdatedAt = time.Now().UTC()
	return m.store.Save(*r)
}
func canonical(h *harness.Handle) string {
	if h == nil {
		return ""
	}
	var v any
	if json.Unmarshal(h.Data, &v) != nil {
		return string(h.Data)
	}
	b, _ := json.Marshal(v)
	return fmt.Sprintf("%d:%s", h.Version, b)
}
func (m *Manager) Import(harnessID string, native harness.NativeSession) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, e := m.resolve(harnessID)
	if e != nil {
		return store.Record{}, e
	}
	for _, r := range m.records {
		if r.Adapter == cfg.Adapter && canonical(r.Resume) == canonical(&native.Handle) {
			if r.Harness != harnessID {
				return store.Record{}, fmt.Errorf("native session is already managed as %s by harness %s; continue that session rather than opening a competing writer", r.ID, r.Harness)
			}
			return clone(r), nil
		}
	}
	now := time.Now().UTC()
	r := &store.Record{SchemaVersion: 1, ID: store.ID(), Harness: harnessID, Adapter: cfg.Adapter, CWD: native.CWD, Title: native.Title, State: "idle", Resume: &native.Handle, EffectiveModel: native.Model, CreatedAt: now, UpdatedAt: now}
	if e = m.store.Save(*r); e != nil {
		return store.Record{}, e
	}
	m.records[r.ID] = r
	return clone(r), nil
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	running := []*active{}
	for _, a := range m.active {
		a.cancel()
		running = append(running, a)
	}
	m.mu.Unlock()
	for _, a := range running {
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
