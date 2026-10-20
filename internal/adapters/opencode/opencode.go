// Package opencode controls an installed OpenCode v2 server. The native server
// owns project configuration and sessions; Atlas owns its helper process.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

type driver struct {
	config      harness.Config
	startup     time.Duration
	permissions []rule
}
type rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}
type modelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func (m modelRef) String() string {
	if m.ID == "" {
		return ""
	}
	return m.ProviderID + "/" + m.ID
}

type nativeHandle struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	APIMajor  int    `json:"apiMajor"`
}
type nativeSession struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	ParentID string   `json:"parentID"`
	Agent    string   `json:"agent"`
	Model    modelRef `json:"model"`
	Outcome  string   `json:"outcome"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Time struct {
		Updated  int64 `json:"updated"`
		Idle     int64 `json:"idle"`
		Archived int64 `json:"archived"`
	} `json:"time"`
	Tokens json.RawMessage `json:"tokens"`
	Cost   float64         `json:"cost"`
}
type page[T any] struct {
	Data   []T `json:"data"`
	Cursor struct {
		Next string `json:"next"`
	} `json:"cursor"`
}
type value[T any] struct {
	Data T `json:"data"`
}
type nativeModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Limit      struct {
		Context int `json:"context"`
	} `json:"limit"`
	Variants []struct {
		ID string `json:"id"`
	} `json:"variants"`
}

// New supports the native v2 API. Unsupported options fail before spawning.
func New(config harness.Config) (harness.Driver, error) {
	d := &driver{config: config, startup: 30 * time.Second}
	if len(d.config.Command) == 0 {
		d.config.Command = []string{"opencode"}
	}
	for key, raw := range config.Options {
		switch key {
		case "permissions":
			if err := json.Unmarshal(raw, &d.permissions); err != nil {
				return nil, fmt.Errorf("opencode permissions: %w", err)
			}
			for _, r := range d.permissions {
				if r.Action == "" || r.Resource == "" || (r.Effect != "allow" && r.Effect != "ask" && r.Effect != "deny") {
					return nil, errors.New("opencode permissions require action, resource, and effect allow/ask/deny")
				}
			}
		case "startupTimeoutSeconds":
			var seconds int
			if err := json.Unmarshal(raw, &seconds); err != nil || seconds < 1 || seconds > 300 {
				return nil, errors.New("opencode startupTimeoutSeconds must be an integer from 1 to 300")
			}
			d.startup = time.Duration(seconds) * time.Second
		default:
			return nil, fmt.Errorf("unknown opencode option %q", key)
		}
	}
	return d, nil
}

func locationQuery(cwd string) string { return "?" + url.Values{"location[directory]": {cwd}}.Encode() }
func sessionPath(id string) string    { return "/api/session/" + url.PathEscape(id) }
func parseModel(id, thinking string) (modelRef, error) {
	provider, model, ok := strings.Cut(id, "/")
	if !ok || provider == "" || model == "" {
		return modelRef{}, fmt.Errorf("OpenCode model must be provider/model, got %q", id)
	}
	return modelRef{ID: model, ProviderID: provider, Variant: thinking}, nil
}
func handleFor(s nativeSession) harness.Handle {
	return harness.Handle{Version: 1, Data: harness.JSON(nativeHandle{SessionID: s.ID, CWD: s.Location.Directory, APIMajor: 2})}
}
func decodeHandle(h harness.Handle) (nativeHandle, error) {
	var n nativeHandle
	if h.Version != 1 {
		return n, fmt.Errorf("unsupported OpenCode handle version %d", h.Version)
	}
	if err := json.Unmarshal(h.Data, &n); err != nil {
		return n, fmt.Errorf("invalid OpenCode handle: %w", err)
	}
	if n.APIMajor != 2 || !strings.HasPrefix(n.SessionID, "ses") || n.CWD == "" {
		return n, errors.New("invalid OpenCode v2 session handle")
	}
	return n, nil
}

func (d *driver) Catalog(ctx context.Context, cwd string) (harness.Catalog, error) {
	s, err := d.start(ctx)
	if err != nil {
		return harness.Catalog{}, err
	}
	defer s.close()
	return s.catalog(ctx, cwd)
}
func (s *server) catalog(ctx context.Context, cwd string) (harness.Catalog, error) {
	if err := s.readyLocation(ctx, cwd); err != nil {
		return harness.Catalog{}, err
	}
	q := locationQuery(cwd)
	var models page[nativeModel]
	if err := s.json(ctx, "GET", "/api/model"+q, nil, &models); err != nil {
		return harness.Catalog{}, err
	}
	var def value[*nativeModel]
	if err := s.json(ctx, "GET", "/api/model/default"+q, nil, &def); err != nil {
		return harness.Catalog{}, err
	}
	var agents page[struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Mode   string `json:"mode"`
		Hidden bool   `json:"hidden"`
	}]
	if err := s.json(ctx, "GET", "/api/agent"+q, nil, &agents); err != nil {
		return harness.Catalog{}, err
	}
	c := harness.Catalog{Models: []harness.Model{}, Source: "OpenCode " + s.version + " native v2 API"}
	for _, m := range models.Data {
		if !m.Enabled {
			continue
		}
		v := harness.Model{ID: m.ProviderID + "/" + m.ID, Name: m.Name, ContextWindow: m.Limit.Context}
		if def.Data != nil {
			v.Default = def.Data.ID == m.ID && def.Data.ProviderID == m.ProviderID
		}
		for _, x := range m.Variants {
			v.Thinking = append(v.Thinking, x.ID)
		}
		c.Models = append(c.Models, v)
	}
	for _, a := range agents.Data {
		if !a.Hidden && a.Mode != "subagent" {
			c.Modes = append(c.Modes, harness.Mode{ID: a.ID, Name: a.Name})
		}
	}
	return c, nil
}
func (d *driver) Discover(ctx context.Context, cwd string, limit int) ([]harness.NativeSession, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	s, err := d.start(ctx)
	if err != nil {
		return nil, err
	}
	defer s.close()
	out := []harness.NativeSession{}
	cursor := ""
	seen := map[string]bool{}
	for len(out) < limit {
		q := url.Values{"limit": {strconv.Itoa(min(100, limit-len(out)))}}
		if cursor != "" {
			q.Set("cursor", cursor)
		} else {
			q.Set("order", "desc")
			q.Set("parentID", "null")
			q.Set("archived", "false")
			if cwd != "" {
				q.Set("directory", cwd)
			}
		}
		var p page[nativeSession]
		if err := s.json(ctx, "GET", "/api/session?"+q.Encode(), nil, &p); err != nil {
			return nil, err
		}
		for _, n := range p.Data {
			if n.ParentID != "" || n.Time.Archived != 0 {
				continue
			}
			out = append(out, harness.NativeSession{ID: n.ID, Title: n.Title, CWD: n.Location.Directory, UpdatedAt: time.UnixMilli(n.Time.Updated), Handle: handleFor(n), Model: n.Model.String()})
			if len(out) == limit {
				break
			}
		}
		if len(p.Data) == 0 || p.Cursor.Next == "" || seen[p.Cursor.Next] {
			break
		}
		cursor = p.Cursor.Next
		seen[cursor] = true
	}
	return out, nil
}

func (d *driver) Run(ctx context.Context, req harness.RunRequest, emit harness.Emit, controls <-chan harness.Control) (result harness.RunResult, err error) {
	if req.CWD == "" {
		return result, errors.New("OpenCode run requires cwd")
	}
	if emit == nil {
		emit = func(harness.Event) error { return nil }
	}
	s, err := d.start(ctx)
	if err != nil {
		return result, err
	}
	defer s.close()
	var session nativeSession
	defer func() {
		if err != nil && session.ID != "" {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.json(stopCtx, "POST", sessionPath(session.ID)+"/interrupt", map[string]any{}, nil)
		}
	}()
	if err = s.readyLocation(ctx, req.CWD); err != nil {
		return result, err
	}
	if err = s.readyMCP(ctx, req.CWD); err != nil {
		return result, err
	}
	selected := req.Model
	if selected == "" {
		selected = d.config.Model
	}
	var model *modelRef
	if selected != "" {
		m, e := parseModel(selected, req.Thinking)
		if e != nil {
			return result, e
		}
		model = &m
	}
	permissions := d.permissions
	switch req.PermissionMode {
	case "", "default":
	case "ask", "allow", "deny":
		permissions = []rule{{Action: "*", Resource: "*", Effect: req.PermissionMode}}
	default:
		return result, fmt.Errorf("OpenCode permission mode %q must be default, ask, allow, or deny", req.PermissionMode)
	}
	if req.Resume != nil {
		n, e := decodeHandle(*req.Resume)
		if e != nil {
			return result, e
		}
		if filepath.Clean(n.CWD) != filepath.Clean(req.CWD) {
			return result, errors.New("OpenCode resume cwd differs from the saved session; moving sessions is not implicit")
		}
		var v value[nativeSession]
		if err = s.json(ctx, "GET", sessionPath(n.SessionID), nil, &v); err != nil {
			return result, err
		}
		session = v.Data
		result.Handle = req.Resume
		result.Model = session.Model.String()
		if err = emit(harness.Event{Type: "persistence", Time: time.Now(), Handle: result.Handle, Model: result.Model}); err != nil {
			return result, err
		}
		if model == nil && req.Thinking != "" {
			m := session.Model
			m.Variant = req.Thinking
			model = &m
		}
		if model != nil {
			if err = s.json(ctx, "POST", sessionPath(session.ID)+"/model", map[string]any{"model": model}, nil); err != nil {
				return result, err
			}
			session.Model = *model
		}
		if req.NativeAgent != "" {
			if err = s.json(ctx, "POST", sessionPath(session.ID)+"/agent", map[string]any{"agent": req.NativeAgent}, nil); err != nil {
				return result, err
			}
		}
		if permissions != nil {
			if err = s.json(ctx, "PATCH", sessionPath(session.ID), map[string]any{"permissions": permissions}, nil); err != nil {
				return result, err
			}
		}
	} else {
		if model == nil && req.Thinking != "" {
			var nativeDefault value[*nativeModel]
			if err = s.json(ctx, "GET", "/api/model/default"+locationQuery(req.CWD), nil, &nativeDefault); err != nil {
				return result, err
			}
			if nativeDefault.Data == nil {
				return result, errors.New("OpenCode has no native default model for the requested thinking variant")
			}
			model = &modelRef{ID: nativeDefault.Data.ID, ProviderID: nativeDefault.Data.ProviderID, Variant: req.Thinking}
		}
		body := map[string]any{"location": map[string]string{"directory": req.CWD}, "title": "Atlas " + req.SessionID}
		if model != nil {
			body["model"] = model
		}
		if req.NativeAgent != "" {
			body["agent"] = req.NativeAgent
		}
		if permissions != nil {
			body["permissions"] = permissions
		}
		var v value[nativeSession]
		if err = s.json(ctx, "POST", "/api/session", body, &v); err != nil {
			return result, err
		}
		session = v.Data
		h := handleFor(session)
		result.Handle = &h
		result.Model = session.Model.String()
		if err = emit(harness.Event{Type: "persistence", Time: time.Now(), Handle: result.Handle, Model: result.Model}); err != nil {
			return result, err
		}
	}
	if session.ID == "" {
		return result, errors.New("OpenCode returned an empty session ID")
	}
	tracker := newTracker(session.ID, emit)
	tracker.cwd = req.CWD
	// Seed native history before prompting so a resumed turn does not replay old
	// assistant output into this turn's result.
	if err = tracker.snapshot(ctx, s, true); err != nil {
		return result, err
	}
	baseline, _, e := s.log(ctx, session.ID, 0)
	if e != nil {
		return result, e
	}
	tracker.sequence = baseline
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	events, streamErrors, closeStream, e := s.events(streamCtx)
	if e != nil {
		return result, e
	}
	defer closeStream()
	var ack value[struct {
		ID string `json:"id"`
	}]
	if err = s.json(ctx, "POST", sessionPath(session.ID)+"/prompt", map[string]any{"text": req.Prompt}, &ack); err != nil {
		return result, err
	}
	if ack.Data.ID == "" {
		return result, errors.New("OpenCode did not return a prompt admission ID")
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-s.exited:
			return result, errors.New("OpenCode helper exited during the turn")
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err = tracker.observe(ev); err != nil {
				return result, err
			}
		case e, ok := <-streamErrors:
			if !ok {
				streamErrors = nil
				continue
			}
			// Persisted snapshots and execution logs recover output and completion
			// if volatile SSE disconnects. Do not infer success from EOF.
			if e != nil {
				if err = emit(harness.Event{Type: "notice", Time: time.Now(), Text: "OpenCode event stream disconnected; recovering from native session state."}); err != nil {
					return result, err
				}
				tracker.streams = map[string]string{}
			}
		case control, ok := <-controls:
			if !ok {
				controls = nil
				continue
			}
			if err = tracker.control(ctx, s, control); err != nil {
				return result, err
			}
		case <-ticker.C:
			if err = tracker.snapshot(ctx, s, false); err != nil {
				return result, err
			}
			if err = tracker.permissions(ctx, s); err != nil {
				return result, err
			}
			sequence, terminal, e := s.log(ctx, session.ID, tracker.sequence)
			if e != nil {
				return result, e
			}
			tracker.sequence = sequence
			if terminal != nil {
				tracker.terminal = terminal
			}
			if tracker.terminal == nil {
				continue
			}
			var active value[map[string]json.RawMessage]
			if err = s.json(ctx, "GET", "/api/session/active", nil, &active); err != nil {
				return result, err
			}
			if _, running := active.Data[session.ID]; running {
				continue
			}
			if err = tracker.snapshot(ctx, s, false); err != nil {
				return result, err
			}
			var current value[nativeSession]
			if err = s.json(ctx, "GET", sessionPath(session.ID), nil, &current); err != nil {
				return result, err
			}
			result.Model = current.Data.Model.String()
			result.Text = tracker.text.String()
			if err = emit(harness.Event{Type: "usage", Time: time.Now(), Model: result.Model, Data: harness.JSON(map[string]any{"tokens": current.Data.Tokens, "cost": current.Data.Cost})}); err != nil {
				return result, err
			}
			switch tracker.terminal.Type {
			case "session.execution.succeeded":
				return result, nil
			case "session.execution.interrupted":
				return result, errors.New("OpenCode execution interrupted")
			default:
				var data struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				_ = json.Unmarshal(tracker.terminal.Data, &data)
				if data.Error.Message == "" {
					data.Error.Message = "unknown provider failure"
				}
				return result, fmt.Errorf("OpenCode execution failed: %s", data.Error.Message)
			}
		}
	}
}
