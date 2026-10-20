// Package claude adapts the installed Claude Code stream-json/control protocol.
// No inference SDK or embedded JavaScript runtime is used.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

type driver struct {
	cfg     harness.Config
	options options
}
type options struct {
	AllowedTools          []string        `json:"allowedTools"`
	DisallowedTools       []string        `json:"disallowedTools"`
	AdditionalDirectories []string        `json:"additionalDirectories"`
	SettingSources        []string        `json:"settingSources"`
	AppendSystemPrompt    string          `json:"appendSystemPrompt"`
	Settings              json.RawMessage `json:"settings"`
	MCPConfig             json.RawMessage `json:"mcpConfig"`
	MaxTurns              int             `json:"maxTurns"`
	MaxBudgetUSD          float64         `json:"maxBudgetUSD"`
	StartupTimeoutMs      int             `json:"startupTimeoutMs"`
}

func New(cfg harness.Config) (harness.Driver, error) {
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"claude"}
	}
	if cfg.Command[0] == "" {
		return nil, errors.New("Claude command is empty")
	}
	data, err := json.Marshal(cfg.Options)
	if err != nil {
		return nil, err
	}
	var opts options
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&opts); err != nil {
		return nil, fmt.Errorf("Claude options: %w", err)
	}
	if opts.MaxTurns < 0 || opts.MaxBudgetUSD < 0 || opts.StartupTimeoutMs < 0 {
		return nil, errors.New("Claude limits must be nonnegative")
	}
	for _, s := range opts.SettingSources {
		if s != "user" && s != "project" && s != "local" {
			return nil, fmt.Errorf("invalid Claude setting source %q", s)
		}
	}
	for name, value := range map[string]json.RawMessage{"settings": opts.Settings, "mcpConfig": opts.MCPConfig} {
		if len(value) > 0 {
			var object map[string]json.RawMessage
			if json.Unmarshal(value, &object) != nil || object == nil {
				return nil, fmt.Errorf("Claude %s must be a JSON object", name)
			}
		}
	}
	return &driver{cfg: cfg, options: opts}, nil
}

func (d *driver) baseArgs() []string {
	args := []string{"--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio"}
	o := d.options
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	for _, dir := range o.AdditionalDirectories {
		args = append(args, "--add-dir", dir)
	}
	if o.SettingSources != nil {
		args = append(args, "--setting-sources", strings.Join(o.SettingSources, ","))
	}
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	if len(o.Settings) > 0 {
		args = append(args, "--settings", string(o.Settings))
	}
	if len(o.MCPConfig) > 0 {
		args = append(args, "--mcp-config", string(o.MCPConfig))
	}
	if o.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(o.MaxTurns))
	}
	if o.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(o.MaxBudgetUSD, 'f', -1, 64))
	}
	return args
}
func (d *driver) startupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := 30 * time.Second
	if d.options.StartupTimeoutMs > 0 {
		timeout = time.Duration(d.options.StartupTimeoutMs) * time.Millisecond
	}
	return context.WithTimeout(ctx, timeout)
}

func (d *driver) Catalog(ctx context.Context, cwd string) (harness.Catalog, error) {
	ctx, cancel := d.startupContext(ctx)
	defer cancel()
	args := append(d.baseArgs(), "--no-session-persistence")
	c, err := start(ctx, d.cfg, cwd, args)
	if err != nil {
		return harness.Catalog{}, err
	}
	defer c.close()
	data, err := c.initialize(ctx)
	if err != nil {
		return harness.Catalog{}, err
	}
	var reply struct {
		Version string `json:"claude_code_version"`
		Models  []struct {
			ID     string   `json:"value"`
			Name   string   `json:"displayName"`
			Effort []string `json:"supportedEffortLevels"`
		} `json:"models"`
	}
	if err = json.Unmarshal(data, &reply); err != nil {
		return harness.Catalog{}, fmt.Errorf("Claude catalog: %w", err)
	}
	cat := harness.Catalog{Source: "Claude Code initialize (" + reply.Version + ")", Models: []harness.Model{}}
	for _, m := range reply.Models {
		if m.ID != "" {
			cat.Models = append(cat.Models, harness.Model{ID: m.ID, Name: m.Name, Default: m.ID == "default", Thinking: m.Effort})
		}
	}
	for _, m := range []string{"manual", "plan", "acceptEdits", "dontAsk", "auto", "bypassPermissions"} {
		cat.Modes = append(cat.Modes, harness.Mode{ID: m, Name: m})
	}
	return cat, nil
}

type nativeHandle struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

func handle(id, cwd string) *harness.Handle {
	return &harness.Handle{Version: 1, Data: harness.JSON(nativeHandle{SessionID: id, CWD: cwd})}
}
func decodeHandle(h *harness.Handle) (nativeHandle, error) {
	var v nativeHandle
	if h == nil || h.Version != 1 {
		return v, errors.New("unsupported Claude persistence handle version")
	}
	if err := json.Unmarshal(h.Data, &v); err != nil {
		return v, fmt.Errorf("Claude persistence handle: %w", err)
	}
	if !validUUID(v.SessionID) {
		return v, errors.New("Claude persistence handle requires a UUID sessionId")
	}
	return v, nil
}
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (d *driver) Run(ctx context.Context, req harness.RunRequest, emit harness.Emit, controls <-chan harness.Control) (result harness.RunResult, err error) {
	if emit == nil {
		emit = func(harness.Event) error { return nil }
	}
	cwd, e := filepath.Abs(req.CWD)
	if e != nil {
		return result, e
	}
	args := append(d.baseArgs(), "--include-partial-messages")
	model := req.Model
	if model == "" {
		model = d.cfg.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.Thinking != "" {
		switch req.Thinking {
		case "low", "medium", "high", "xhigh", "max":
			args = append(args, "--effort", req.Thinking)
		default:
			return result, fmt.Errorf("invalid Claude thinking effort %q", req.Thinking)
		}
	}
	if req.NativeAgent != "" {
		args = append(args, "--agent", req.NativeAgent)
	}
	if req.PermissionMode != "" {
		mode := req.PermissionMode
		if mode == "default" {
			mode = "manual"
		}
		switch mode {
		case "manual", "plan", "acceptEdits", "dontAsk", "auto":
		case "bypassPermissions":
			args = append(args, "--allow-dangerously-skip-permissions")
		default:
			return result, fmt.Errorf("invalid Claude permission mode %q", mode)
		}
		args = append(args, "--permission-mode", mode)
	}
	if req.Resume != nil {
		h, e := decodeHandle(req.Resume)
		if e != nil {
			return result, e
		}
		args = append(args, "--resume", h.SessionID)
		result.Handle = handle(h.SessionID, cwd)
	} else {
		id, e := newUUID()
		if e != nil {
			return result, e
		}
		args = append(args, "--session-id", id)
	}
	c, e := start(ctx, d.cfg, cwd, args)
	if e != nil {
		return result, e
	}
	defer c.close()
	initCtx, cancel := d.startupContext(ctx)
	_, e = c.initialize(initCtx)
	cancel()
	if e != nil {
		return result, e
	}
	if e = c.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": req.Prompt}, "session_id": "", "parent_tool_use_id": nil}); e != nil {
		return result, e
	}
	s := streamState{cwd: cwd, result: &result, emit: emit, pending: map[string]permissionRequest{}}
	for {
		select {
		case <-ctx.Done():
			// Native interruption is requested before close cancels/reaps the
			// group. No next turn can start until this Run returns.
			_ = c.request("atlas-interrupt", "interrupt", map[string]any{"cancel_queued": true})
			return result, ctx.Err()
		case control, ok := <-controls:
			if !ok {
				controls = nil
				continue
			}
			if e = s.control(c, control); e != nil {
				return result, e
			}
		case f, ok := <-c.frames:
			if !ok {
				e = c.exitError()
				if errors.Is(e, io.EOF) {
					e = errors.New("Claude exited before a terminal result")
				}
				return result, e
			}
			if f.err != nil {
				return result, fmt.Errorf("Claude stream: %w", f.err)
			}
			done, e := s.consume(c, f.data)
			if e != nil {
				return result, e
			}
			if done {
				return result, nil
			}
		}
	}
}

type permissionRequest struct {
	Tool        string          `json:"tool_name"`
	Input       json.RawMessage `json:"input"`
	Description string          `json:"description"`
	Title       string          `json:"title"`
	ToolUseID   string          `json:"tool_use_id"`
	Subtype     string          `json:"subtype"`
}
type streamState struct {
	cwd               string
	result            *harness.RunResult
	emit              harness.Emit
	pending           map[string]permissionRequest
	streamedText      bool
	streamedReasoning bool
	text              strings.Builder
}

func (s *streamState) publish(e harness.Event) error { e.Time = time.Now().UTC(); return s.emit(e) }
func (s *streamState) control(c *connection, control harness.Control) error {
	if control.Type != "permission" {
		return s.publish(harness.Event{Type: "notice", Text: "Claude active-turn steering is unsupported; send a new prompt after this turn.", Data: harness.JSON(map[string]any{"code": "unsupported", "control": control.Type})})
	}
	p, ok := s.pending[control.ID]
	if !ok {
		return s.publish(harness.Event{Type: "notice", Text: "Claude permission is no longer pending", Data: harness.JSON(map[string]any{"requestId": control.ID})})
	}
	response := map[string]any{"behavior": "deny", "message": "Denied by Atlas user", "toolUseID": p.ToolUseID}
	if control.Text != "" {
		response["message"] = control.Text
	}
	if control.Allow {
		updatedInput, err := permissionInput(p, control.Text)
		if err != nil {
			// Keep the permission pending so the caller can correct the answer.
			return s.publish(harness.Event{Type: "notice", Text: err.Error(), Data: harness.JSON(map[string]any{"requestId": control.ID, "code": "invalid_answer"})})
		}
		response = map[string]any{"behavior": "allow", "updatedInput": updatedInput, "toolUseID": p.ToolUseID}
	}
	if err := c.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": control.ID, "response": response}}); err != nil {
		return err
	}
	delete(s.pending, control.ID)
	return s.publish(harness.Event{Type: "permission_resolved", Data: harness.JSON(map[string]any{"id": control.ID, "allowed": control.Allow})})
}

// Question tools require answers in updatedInput, whereas ordinary approvals
// preserve the exact native tool input. A single question accepts plain text;
// multiple questions use a JSON object keyed by the question text.
func permissionInput(p permissionRequest, text string) (json.RawMessage, error) {
	if p.Tool != "AskUserQuestion" {
		return p.Input, nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(p.Input, &input); err != nil {
		return nil, fmt.Errorf("invalid Claude question input: %w", err)
	}
	var questions []struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(input["questions"], &questions); err != nil || len(questions) == 0 {
		return nil, errors.New("Claude question request has no readable questions")
	}
	answers := map[string]string{}
	if json.Unmarshal([]byte(text), &answers) != nil || answers == nil {
		if len(questions) != 1 || strings.TrimSpace(text) == "" {
			return nil, errors.New("answer Claude questions using text for one question, or a JSON object keyed by question text")
		}
		answers = map[string]string{questions[0].Question: text}
	}
	for _, q := range questions {
		if _, ok := answers[q.Question]; !ok {
			return nil, fmt.Errorf("missing answer to Claude question %q", q.Question)
		}
	}
	input["answers"] = harness.JSON(answers)
	return json.Marshal(input)
}

func (s *streamState) consume(c *connection, line json.RawMessage) (bool, error) {
	var msg struct {
		Type      string            `json:"type"`
		Subtype   string            `json:"subtype"`
		SessionID string            `json:"session_id"`
		Model     string            `json:"model"`
		ID        string            `json:"request_id"`
		Parent    *string           `json:"parent_tool_use_id"`
		Result    string            `json:"result"`
		IsError   bool              `json:"is_error"`
		Errors    []string          `json:"errors"`
		Request   permissionRequest `json:"request"`
		Message   struct {
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Error   string          `json:"error"`
		} `json:"message"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
			Block json.RawMessage `json:"content_block"`
		} `json:"event"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return false, fmt.Errorf("invalid Claude stream JSON: %w", err)
	}
	if msg.Parent != nil {
		return false, s.publish(harness.Event{Type: "tool", Data: line})
	}
	if msg.SessionID != "" && (s.result.Handle == nil || !sameSession(s.result.Handle, msg.SessionID)) {
		s.result.Handle = handle(msg.SessionID, s.cwd)
		if err := s.publish(harness.Event{Type: "persistence", Handle: s.result.Handle}); err != nil {
			return false, err
		}
	}
	switch msg.Type {
	case "system":
		if msg.Subtype == "init" {
			s.result.Model = msg.Model
			return false, s.publish(harness.Event{Type: "config", Model: msg.Model, Data: line})
		}
		return false, s.publish(harness.Event{Type: "notice", Data: line})
	case "stream_event":
		if msg.Event.Type == "message_start" {
			s.streamedText = false
			s.streamedReasoning = false
		}
		if msg.Event.Type == "content_block_delta" {
			switch msg.Event.Delta.Type {
			case "text_delta":
				s.streamedText = true
				s.text.WriteString(msg.Event.Delta.Text)
				return false, s.publish(harness.Event{Type: "text", Text: msg.Event.Delta.Text})
			case "thinking_delta":
				s.streamedReasoning = true
				return false, s.publish(harness.Event{Type: "reasoning", Text: msg.Event.Delta.Thinking})
			}
		}
	case "assistant":
		var blocks []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(msg.Message.Content, &blocks); err != nil {
			return false, fmt.Errorf("Claude assistant content: %w", err)
		}
		if msg.Message.Model != "" {
			s.result.Model = msg.Message.Model
		}
		for i, b := range blocks {
			var event harness.Event
			switch b.Type {
			case "text":
				if s.streamedText {
					continue
				}
				s.text.WriteString(b.Text)
				event = harness.Event{Type: "text", Text: b.Text}
			case "thinking":
				if s.streamedReasoning {
					continue
				}
				event = harness.Event{Type: "reasoning", Text: b.Thinking}
			case "tool_use":
				var raw []json.RawMessage
				_ = json.Unmarshal(msg.Message.Content, &raw)
				event = harness.Event{Type: "tool", Data: raw[i]}
			default:
				continue
			}
			if err := s.publish(event); err != nil {
				return false, err
			}
		}
	case "user":
		var blocks []json.RawMessage
		if json.Unmarshal(msg.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				var kind struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(b, &kind)
				if kind.Type == "tool_result" {
					if err := s.publish(harness.Event{Type: "tool", Data: b}); err != nil {
						return false, err
					}
				}
			}
		}
	case "control_request":
		if msg.Request.Subtype != "can_use_tool" {
			return false, c.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": msg.ID, "error": "Unsupported Atlas Claude control request: " + msg.Request.Subtype}})
		}
		s.pending[msg.ID] = msg.Request
		description := msg.Request.Description
		if description == "" {
			description = msg.Request.Title
		}
		return false, s.publish(harness.Event{Type: "permission", Permission: &harness.Permission{ID: msg.ID, Tool: msg.Request.Tool, Input: msg.Request.Input, Description: description, Choices: []string{"allow", "deny"}}})
	case "control_cancel_request":
		delete(s.pending, msg.ID)
		return false, s.publish(harness.Event{Type: "permission_resolved", Data: harness.JSON(map[string]any{"id": msg.ID, "canceled": true})})
	case "result":
		if msg.Result != "" {
			s.result.Text = msg.Result
		} else {
			s.result.Text = s.text.String()
		}
		if err := s.publish(harness.Event{Type: "usage", Data: line}); err != nil {
			return false, err
		}
		if msg.IsError || msg.Subtype != "success" {
			detail := strings.Join(msg.Errors, "; ")
			if detail == "" {
				detail = msg.Result
			}
			return false, fmt.Errorf("Claude turn failed (%s): %s", msg.Subtype, detail)
		}
		if s.text.Len() == 0 && msg.Result != "" {
			// Side-effect commands and native versions without assistant text
			// frames can return only a result. Preserve their visible output.
			if err := s.publish(harness.Event{Type: "text", Text: msg.Result}); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}
func sameSession(h *harness.Handle, id string) bool {
	v, err := decodeHandle(h)
	return err == nil && v.SessionID == id
}
