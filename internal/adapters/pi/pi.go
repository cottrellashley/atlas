// Package pi integrates Pi 1.3's JSONL RPC protocol. Native project resources
// remain owned by Pi; Atlas supplies cwd, explicit options, and session identity.
package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

type options struct {
	SessionDir string `json:"sessionDir"`
	Approve    *bool  `json:"approve"`
	Offline    bool   `json:"offline"`
}
type Driver struct {
	cfg  harness.Config
	opts options
}

func New(cfg harness.Config) (harness.Driver, error) {
	if cfg.Adapter == "" {
		cfg.Adapter = "pi"
	}
	var opts options
	for key, value := range cfg.Options {
		var target any
		switch key {
		case "sessionDir":
			target = &opts.SessionDir
		case "approve":
			target = &opts.Approve
		case "offline":
			target = &opts.Offline
		default:
			return nil, fmt.Errorf("unknown Pi option %q", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return nil, fmt.Errorf("Pi option %s: %w", key, err)
		}
	}
	return &Driver{cfg: cfg, opts: opts}, nil
}

type nativeHandle struct {
	SessionID   string `json:"sessionId"`
	SessionFile string `json:"sessionFile"`
}
type nativeModel struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	Reasoning     bool   `json:"reasoning"`
}

func (m nativeModel) qualified() string {
	if m.ID == "" {
		return ""
	}
	if m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

type state struct {
	SessionID   string      `json:"sessionId"`
	SessionFile string      `json:"sessionFile"`
	Model       nativeModel `json:"model"`
	Thinking    string      `json:"thinkingLevel"`
	Streaming   bool        `json:"isStreaming"`
	Compacting  bool        `json:"isCompacting"`
	Pending     int         `json:"pendingMessageCount"`
}

func (d *Driver) baseArgs() []string {
	args := []string{"--mode", "rpc"}
	if d.opts.Approve != nil {
		if *d.opts.Approve {
			args = append(args, "--approve")
		} else {
			args = append(args, "--no-approve")
		}
	}
	if d.opts.Offline {
		args = append(args, "--offline")
	}
	if d.opts.SessionDir != "" {
		args = append(args, "--session-dir", d.opts.SessionDir)
	}
	return args
}

func (d *Driver) Catalog(ctx context.Context, cwd string) (catalog harness.Catalog, err error) {
	if err = ctx.Err(); err != nil {
		return catalog, err
	}
	args := append(d.baseArgs(), "--no-session")
	if d.cfg.Model != "" {
		args = append(args, "--model", d.cfg.Model)
	}
	p, err := startRPC(d.cfg, cwd, args)
	if err != nil {
		return catalog, err
	}
	defer func() {
		closeErr := p.close()
		if err == nil {
			err = closeErr
		}
	}()
	if err = p.send(map[string]any{"id": "catalog", "type": "get_available_models"}); err != nil {
		return catalog, err
	}
	if err = p.send(map[string]any{"id": "state", "type": "get_state"}); err != nil {
		return catalog, err
	}
	var models []nativeModel
	var selected string
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case <-ctx.Done():
			return catalog, ctx.Err()
		case r, ok := <-p.records:
			if !ok {
				return catalog, p.exitedError()
			}
			if r.Type == "extension_ui_request" {
				if err = cancelProbeDialog(p, r); err != nil {
					return catalog, err
				}
				continue
			}
			if r.Type != "response" {
				continue
			}
			if r.ID != "catalog" && r.ID != "state" {
				continue
			}
			if err = responseError(r); err != nil {
				return catalog, err
			}
			seen[r.ID] = true
			if r.ID == "catalog" {
				var data struct {
					Models []nativeModel `json:"models"`
				}
				if err = json.Unmarshal(r.Data, &data); err != nil {
					return catalog, err
				}
				models = data.Models
			} else {
				var s state
				if err = json.Unmarshal(r.Data, &s); err != nil {
					return catalog, err
				}
				selected = s.Model.qualified()
			}
		}
	}
	catalog.Source = "pi RPC get_available_models (installed runtime)"
	catalog.Models = make([]harness.Model, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		catalog.Models = append(catalog.Models, harness.Model{ID: m.qualified(), Name: m.Name, Default: m.qualified() == selected, ContextWindow: m.ContextWindow})
	}
	sort.Slice(catalog.Models, func(i, j int) bool { return catalog.Models[i].ID < catalog.Models[j].ID })
	// Thinking levels are model-specific. Do not advertise an invented universal list.
	return catalog, nil
}

func decodeHandle(h harness.Handle) (nativeHandle, error) {
	var n nativeHandle
	if h.Version != 1 {
		return n, fmt.Errorf("unsupported Pi handle version %d", h.Version)
	}
	if err := json.Unmarshal(h.Data, &n); err != nil {
		return n, fmt.Errorf("invalid Pi handle: %w", err)
	}
	if n.SessionFile == "" || !filepath.IsAbs(n.SessionFile) {
		return n, errors.New("Pi resume requires an absolute native session file")
	}
	return n, nil
}

func (d *Driver) Run(ctx context.Context, req harness.RunRequest, emit harness.Emit, controls <-chan harness.Control) (result harness.RunResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if req.NativeAgent != "" {
		return result, fmt.Errorf("Pi native-agent selection: %w", harness.ErrUnsupported)
	}
	if req.PermissionMode != "" {
		return result, fmt.Errorf("Pi has no native permission modes; extension dialogs are supported: %w", harness.ErrUnsupported)
	}
	args := d.baseArgs()
	var expectedSessionID string
	if req.Resume != nil {
		n, decodeErr := decodeHandle(*req.Resume)
		if decodeErr != nil {
			return result, decodeErr
		}
		info, statErr := os.Stat(n.SessionFile)
		if statErr != nil {
			return result, fmt.Errorf("Pi resume file: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			return result, errors.New("Pi resume handle is not a regular session file")
		}
		args = append(args, "--session", n.SessionFile)
		expectedSessionID = n.SessionID
		result.Handle = req.Resume
	} else if req.SessionID != "" {
		args = append(args, "--session-id", req.SessionID)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	} else if req.Resume == nil && d.cfg.Model != "" {
		args = append(args, "--model", d.cfg.Model)
	}
	if req.Thinking != "" {
		args = append(args, "--thinking", req.Thinking)
	}
	p, err := startRPC(d.cfg, req.CWD, args)
	if err != nil {
		return result, err
	}
	defer func() {
		closeErr := p.close()
		if err == nil {
			err = closeErr
		}
	}()
	if emit == nil {
		emit = func(harness.Event) error { return nil }
	}
	run := runState{result: &result, emit: emit, p: p, pending: map[string]dialog{}, expiry: make(chan string, 128), expiryStop: make(chan struct{})}
	defer func() {
		close(run.expiryStop)
		for _, timer := range run.expiryTimers {
			timer.Stop()
		}
	}()
	if err = p.send(map[string]any{"id": "state-initial", "type": "get_state"}); err != nil {
		return result, err
	}
	var accepted, settled, finalRequested bool
	var cancelErr error
	var cancelTimer <-chan time.Time
	cancelSignal := ctx.Done()
	var sequence int
	finish := func() error {
		if finalRequested {
			return nil
		}
		finalRequested = true
		return p.send(map[string]any{"id": "state-final", "type": "get_state"})
	}
	for {
		select {
		case id := <-run.expiry:
			if _, exists := run.pending[id]; exists {
				delete(run.pending, id)
				if err = run.publish(harness.Event{Type: "permission_resolved", Data: harness.JSON(map[string]any{"id": id, "timeout": true})}); err != nil {
					return result, err
				}
			}
		case <-cancelSignal:
			cancelErr = ctx.Err()
			cancelSignal = nil
			for id := range run.pending {
				_ = p.send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
			}
			if err = p.send(map[string]any{"id": "cancel-clear", "type": "clear_queue"}); err != nil {
				return result, cancelErr
			}
			cancelTimer = time.After(3 * time.Second)
		case <-cancelTimer:
			return result, cancelErr
		case control, ok := <-controls:
			if !ok {
				controls = nil
				continue
			}
			if cancelErr != nil {
				continue
			}
			switch control.Type {
			case "permission":
				if err = run.respond(control); err != nil {
					return result, err
				}
			case "steer":
				sequence++
				if err = p.send(map[string]any{"id": "steer-" + strconv.Itoa(sequence), "type": "steer", "message": control.Text}); err != nil {
					return result, err
				}
			default:
				return result, fmt.Errorf("unsupported Pi control %q", control.Type)
			}
		case r, ok := <-p.records:
			if !ok {
				if cancelErr != nil {
					return result, cancelErr
				}
				return result, p.exitedError()
			}
			if r.Type == "response" {
				if r.ID == "cancel-clear" {
					if err = p.send(map[string]any{"id": "cancel-abort", "type": "abort"}); err != nil {
						return result, cancelErr
					}
					continue
				}
				if r.ID == "cancel-abort" {
					return result, cancelErr
				}
				if cancelErr != nil {
					continue
				}
				if err = responseError(r); err != nil {
					return result, err
				}
				switch r.ID {
				case "state-initial":
					if expectedSessionID != "" {
						var native state
						if err = json.Unmarshal(r.Data, &native); err != nil {
							return result, err
						}
						if native.SessionID != expectedSessionID {
							return result, errors.New("Pi resumed a different native session than the persisted handle")
						}
					}
					if err = run.applyState(r.Data); err != nil {
						return result, err
					}
					if err = p.send(map[string]any{"id": "prompt", "type": "prompt", "message": req.Prompt}); err != nil {
						return result, err
					}
				case "prompt":
					accepted = true
					var disposition struct {
						Disposition string `json:"disposition"`
					}
					if err = json.Unmarshal(r.Data, &disposition); err != nil {
						return result, err
					}
					if disposition.Disposition == "handled" || settled {
						if err = finish(); err != nil {
							return result, err
						}
					}
				case "state-final":
					if err = run.applyState(r.Data); err != nil {
						return result, err
					}
					var final state
					if err = json.Unmarshal(r.Data, &final); err != nil {
						return result, err
					}
					if final.Streaming || final.Compacting || final.Pending > 0 {
						// An extension command can be handled while independently
						// starting work. Keep owning it until native settlement.
						finalRequested = false
						settled = false
						continue
					}
					return result, run.failure
				}
				continue
			}
			if err = run.event(r); err != nil {
				return result, err
			}
			if r.Type == "agent_settled" {
				if cancelErr != nil {
					return result, cancelErr
				}
				settled = true
				var event struct {
					Aborted bool `json:"aborted"`
				}
				_ = json.Unmarshal(r.Raw, &event)
				if event.Aborted {
					run.failure = errors.New("Pi run aborted")
				}
				if accepted {
					if err = finish(); err != nil {
						return result, err
					}
				}
			}
		}
	}
}

type dialog struct {
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Options []string `json:"options"`
	Timeout int      `json:"timeout"`
}
type runState struct {
	result       *harness.RunResult
	emit         harness.Emit
	p            *rpcProcess
	pending      map[string]dialog
	failure      error
	streamedText bool
	expiry       chan string
	expiryStop   chan struct{}
	expiryTimers []*time.Timer
}

func (r *runState) publish(event harness.Event) error {
	event.Time = time.Now().UTC()
	return r.emit(event)
}
func (r *runState) applyState(raw json.RawMessage) error {
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("decode Pi state: %w", err)
	}
	if s.SessionFile == "" || s.SessionID == "" {
		return errors.New("Pi did not report a persistent session identity")
	}
	h := harness.Handle{Version: 1, Data: harness.JSON(nativeHandle{SessionID: s.SessionID, SessionFile: s.SessionFile})}
	changed := r.result.Handle == nil || string(r.result.Handle.Data) != string(h.Data)
	r.result.Handle = &h
	r.result.Model = s.Model.qualified()
	if changed {
		if err := r.publish(harness.Event{Type: "persistence", Handle: &h, Model: r.result.Model}); err != nil {
			return err
		}
	}
	return r.publish(harness.Event{Type: "config", Model: r.result.Model, Data: harness.JSON(map[string]any{"thinking": s.Thinking})})
}

func (r *runState) event(event record) error {
	switch event.Type {
	case "message_start":
		var e struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal(event.Raw, &e)
		if e.Message.Role == "assistant" {
			r.streamedText = false
		}
	case "message_update":
		var e struct {
			Update struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(event.Raw, &e); err != nil {
			return err
		}
		if e.Update.Type == "text_delta" {
			r.streamedText = true
			if err := r.publish(harness.Event{Type: "text", Text: e.Update.Delta}); err != nil {
				return err
			}
		}
		if e.Update.Type == "thinking_delta" {
			if err := r.publish(harness.Event{Type: "reasoning", Text: e.Update.Delta}); err != nil {
				return err
			}
		}
		if len(e.Usage) > 0 {
			return r.publish(harness.Event{Type: "usage", Data: e.Usage})
		}
	case "message_end":
		var e struct {
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				StopReason   string          `json:"stopReason"`
				ErrorMessage string          `json:"errorMessage"`
				Usage        json.RawMessage `json:"usage"`
				Model        string          `json:"model"`
				Provider     string          `json:"provider"`
			} `json:"message"`
		}
		// User messages can have string content; only decode assistant bodies.
		var envelope struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(event.Raw, &envelope); err != nil {
			return err
		}
		var role struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(envelope.Message, &role)
		if role.Role != "assistant" {
			return nil
		}
		if err := json.Unmarshal(event.Raw, &e); err != nil {
			return err
		}
		var text strings.Builder
		for _, block := range e.Message.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		r.result.Text = text.String()
		if e.Message.StopReason == "error" || e.Message.StopReason == "aborted" {
			message := e.Message.ErrorMessage
			if message == "" {
				message = "Pi assistant ended with " + e.Message.StopReason
			}
			r.failure = errors.New(message)
		} else {
			r.failure = nil
		}
		if !r.streamedText && r.result.Text != "" {
			if err := r.publish(harness.Event{Type: "text", Text: r.result.Text}); err != nil {
				return err
			}
		}
		if len(e.Message.Usage) > 0 {
			return r.publish(harness.Event{Type: "usage", Data: e.Message.Usage})
		}
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		return r.publish(harness.Event{Type: "tool", Data: event.Raw})
	case "extension_ui_request":
		var d dialog
		if err := json.Unmarshal(event.Raw, &d); err != nil {
			return err
		}
		switch d.Method {
		case "select", "confirm", "input", "editor":
			r.pending[d.ID] = d
			if d.Timeout > 0 {
				// Native Pi resolves its own timed dialogs. Match that lifecycle
				// in the manager without sending a second native answer.
				r.expiryTimers = append(r.expiryTimers, time.AfterFunc(time.Duration(d.Timeout)*time.Millisecond, func() {
					select {
					case r.expiry <- d.ID:
					case <-r.expiryStop:
					}
				}))
			}
			return r.publish(harness.Event{Type: "permission", Permission: &harness.Permission{ID: d.ID, Tool: d.Method, Description: strings.TrimSpace(d.Title + "\n" + d.Message), Input: event.Raw, Choices: d.Options}})
		default:
			return r.publish(harness.Event{Type: "notice", Data: event.Raw, Text: d.Message})
		}
	case "thinking_level_changed", "session_info_changed":
		return r.publish(harness.Event{Type: "config", Data: event.Raw})
	case "auto_retry_end":
		var e struct {
			Success    bool   `json:"success"`
			FinalError string `json:"finalError"`
		}
		_ = json.Unmarshal(event.Raw, &e)
		if !e.Success && e.FinalError != "" {
			r.failure = errors.New(e.FinalError)
		}
		return r.publish(harness.Event{Type: "notice", Data: event.Raw})
	case "auto_retry_start", "compaction_start", "compaction_end", "extension_error", "queue_update", "entry_appended", "summarization_retry_scheduled", "summarization_retry_finished":
		return r.publish(harness.Event{Type: "notice", Data: event.Raw})
	}
	return nil
}

func (r *runState) respond(control harness.Control) error {
	d, ok := r.pending[control.ID]
	if !ok {
		return fmt.Errorf("unknown Pi permission request %q", control.ID)
	}
	response := map[string]any{"type": "extension_ui_response", "id": control.ID}
	if !control.Allow {
		response["cancelled"] = true
	} else {
		switch d.Method {
		case "confirm":
			response["confirmed"] = true
		case "select":
			found := false
			for _, option := range d.Options {
				if option == control.Text {
					found = true
					break
				}
			}
			if !found {
				return errors.New("Pi select response must exactly match one offered choice")
			}
			response["value"] = control.Text
		default:
			response["value"] = control.Text
		}
	}
	if err := r.p.send(response); err != nil {
		return err
	}
	delete(r.pending, control.ID)
	return r.publish(harness.Event{Type: "permission_resolved", Data: harness.JSON(map[string]any{"id": control.ID, "allow": control.Allow})})
}

func cancelProbeDialog(p *rpcProcess, r record) error {
	var d dialog
	if err := json.Unmarshal(r.Raw, &d); err != nil {
		return err
	}
	switch d.Method {
	case "confirm", "select", "input", "editor":
		return p.send(map[string]any{"type": "extension_ui_response", "id": d.ID, "cancelled": true})
	}
	return nil
}
