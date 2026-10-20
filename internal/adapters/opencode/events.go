package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

type tracker struct {
	id       string
	cwd      string
	emit     harness.Emit
	sequence int64
	terminal *nativeEvent
	content  map[string]string
	streams  map[string]string
	pending  map[string]pendingRequest
	owners   map[string]bool
	text     strings.Builder
}

type pendingRequest struct{ Kind, SessionID string }

func newTracker(id string, emit harness.Emit) *tracker {
	return &tracker{id: id, emit: emit, content: map[string]string{}, streams: map[string]string{}, pending: map[string]pendingRequest{}, owners: map[string]bool{id: true}}
}
func partKey(message, kind string, ordinal int) string {
	return fmt.Sprintf("%s:%s:%d", message, kind, ordinal)
}
func (t *tracker) appendText(key, kind, text string, seed bool) error {
	previous := t.content[key]
	if len(text) <= len(previous) {
		return nil
	}
	if !strings.HasPrefix(text, previous) {
		return errors.New("OpenCode revised already emitted message content")
	}
	t.content[key] = text
	if seed {
		return nil
	}
	delta := text[len(previous):]
	if kind == "text" {
		t.text.WriteString(delta)
	}
	return t.emit(harness.Event{Type: kind, Text: delta, Time: time.Now(), Data: harness.JSON(map[string]string{"partId": key})})
}
func (t *tracker) observe(e nativeEvent) error {
	var owner struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(e.Data, &owner); err != nil {
		return err
	}
	if owner.SessionID != t.id {
		return nil
	}
	switch e.Type {
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		if e.Durable.Seq > t.sequence {
			copy := e
			t.terminal = &copy
		}
		return nil
	case "session.execution.started":
		if e.Durable.Seq > t.sequence {
			t.terminal = nil
		}
		return nil
	case "session.text.started", "session.reasoning.started", "session.text.delta", "session.reasoning.delta":
	default:
		return nil
	}
	var data struct {
		SessionID          string `json:"sessionID"`
		AssistantMessageID string `json:"assistantMessageID"`
		Ordinal            int    `json:"ordinal"`
		Delta              string `json:"delta"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return err
	}
	if data.SessionID != t.id {
		return nil
	}
	kind := "text"
	if strings.Contains(e.Type, "reasoning") {
		kind = "reasoning"
	}
	key := partKey(data.AssistantMessageID, kind, data.Ordinal)
	switch e.Type {
	case "session.text.started", "session.reasoning.started":
		t.streams[key] = ""
	case "session.text.delta", "session.reasoning.delta":
		previous, ok := t.streams[key]
		if !ok {
			return nil
		}
		text := previous + data.Delta
		t.streams[key] = text
		return t.appendText(key, kind, text, false)
	}
	return nil
}

type message struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Model   modelRef          `json:"model"`
	Content []json.RawMessage `json:"content"`
}

func (t *tracker) snapshot(ctx context.Context, s *server, seed bool) error {
	cursor := ""
	seen := map[string]bool{}
	for {
		q := url.Values{"limit": {"100"}}
		if cursor == "" {
			q.Set("order", "asc")
		} else {
			q.Set("cursor", cursor)
		}
		var messages page[message]
		if err := s.json(ctx, "GET", sessionPath(t.id)+"/message?"+q.Encode(), nil, &messages); err != nil {
			return err
		}
		for _, m := range messages.Data {
			if m.Type != "assistant" {
				continue
			}
			ordinals := map[string]int{}
			for index, raw := range m.Content {
				var part struct {
					Type string `json:"type"`
					Text string `json:"text"`
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(raw, &part); err != nil {
					return err
				}
				if part.Type == "text" || part.Type == "reasoning" {
					ordinal := ordinals[part.Type]
					ordinals[part.Type]++
					if err := t.appendText(partKey(m.ID, part.Type, ordinal), part.Type, part.Text, seed); err != nil {
						return err
					}
				} else if part.Type == "tool" {
					key := fmt.Sprintf("%s:tool:%d", m.ID, index)
					if t.content[key] == string(raw) {
						continue
					}
					t.content[key] = string(raw)
					if !seed {
						if err := t.emit(harness.Event{Type: "tool", Time: time.Now(), Text: part.Name, Data: raw}); err != nil {
							return err
						}
					}
				}
			}
		}
		if len(messages.Data) == 0 || messages.Cursor.Next == "" || seen[messages.Cursor.Next] {
			return nil
		}
		cursor = messages.Cursor.Next
		seen[cursor] = true
	}
}
func (t *tracker) permissions(ctx context.Context, s *server) error {
	var list page[struct {
		ID        string          `json:"id"`
		SessionID string          `json:"sessionID"`
		Action    string          `json:"action"`
		Resources []string        `json:"resources"`
		Message   string          `json:"message"`
		Metadata  json.RawMessage `json:"metadata"`
	}]
	if err := s.json(ctx, "GET", "/api/permission/request"+locationQuery(t.cwd), nil, &list); err != nil {
		return err
	}
	for _, p := range list.Data {
		if _, ok := t.pending[p.ID]; ok {
			continue
		}
		owned, err := t.ownsSession(ctx, s, p.SessionID)
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		t.pending[p.ID] = pendingRequest{Kind: "permission", SessionID: p.SessionID}
		description := p.Message
		if description == "" {
			description = strings.Join(p.Resources, "\n")
		}
		if err := t.emit(harness.Event{Type: "permission", Time: time.Now(), Data: harness.JSON(map[string]string{"nativeSessionId": p.SessionID}), Permission: &harness.Permission{ID: p.ID, Tool: p.Action, Description: description, Input: p.Metadata, Choices: []string{"allow", "deny"}}}); err != nil {
			return err
		}
	}
	var forms page[struct {
		ID        string          `json:"id"`
		SessionID string          `json:"sessionID"`
		Title     string          `json:"title"`
		Fields    json.RawMessage `json:"fields"`
	}]
	if err := s.json(ctx, "GET", "/api/form"+locationQuery(t.cwd), nil, &forms); err != nil {
		return err
	}
	for _, f := range forms.Data {
		if _, ok := t.pending[f.ID]; ok {
			continue
		}
		owned, err := t.ownsSession(ctx, s, f.SessionID)
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		t.pending[f.ID] = pendingRequest{Kind: "form", SessionID: f.SessionID}
		if err := t.emit(harness.Event{Type: "permission", Time: time.Now(), Data: harness.JSON(map[string]string{"nativeSessionId": f.SessionID}), Permission: &harness.Permission{ID: f.ID, Tool: "question", Description: f.Title, Input: f.Fields, Choices: []string{"answer-json", "deny"}}}); err != nil {
			return err
		}
	}
	return nil
}

// Native subagents own separate sessions and can request permissions/forms.
// Resolve parent links before accepting a location-wide request, so another
// root session in the same project can never receive an Atlas reply here.
func (t *tracker) ownsSession(ctx context.Context, s *server, id string) (bool, error) {
	trail := []string{}
	seen := map[string]bool{}
	owned := false
	for id != "" {
		if cached, ok := t.owners[id]; ok {
			owned = cached
			break
		}
		if seen[id] || len(trail) >= 128 {
			return false, errors.New("OpenCode session ancestry is cyclic or too deep")
		}
		seen[id] = true
		trail = append(trail, id)
		var session value[nativeSession]
		if err := s.json(ctx, "GET", sessionPath(id), nil, &session); err != nil {
			return false, err
		}
		if session.Data.ID != id {
			return false, errors.New("OpenCode returned a mismatched session while checking request ownership")
		}
		id = session.Data.ParentID
	}
	for _, id := range trail {
		t.owners[id] = owned
	}
	return owned, nil
}

func (t *tracker) control(ctx context.Context, s *server, c harness.Control) error {
	if c.Type == "steer" {
		return s.json(ctx, "POST", sessionPath(t.id)+"/prompt", map[string]string{"text": c.Text, "delivery": "steer"}, nil)
	}
	if c.Type != "permission" {
		return fmt.Errorf("OpenCode control %q: %w", c.Type, harness.ErrUnsupported)
	}
	pending, ok := t.pending[c.ID]
	if !ok {
		return fmt.Errorf("OpenCode permission %q is not pending", c.ID)
	}
	if pending.Kind == "form" {
		path := sessionPath(pending.SessionID) + "/form/" + url.PathEscape(c.ID)
		if !c.Allow {
			if err := s.json(ctx, "DELETE", path, nil, nil); err != nil {
				return err
			}
		} else {
			var answer map[string]any
			if err := json.Unmarshal([]byte(c.Text), &answer); err != nil {
				return fmt.Errorf("OpenCode question expects a JSON object mapping field keys to answers: %w", err)
			}
			if err := s.json(ctx, "POST", path+"/reply", map[string]any{"answer": answer}, nil); err != nil {
				return err
			}
		}
	} else {
		decision := "reject"
		if c.Allow {
			decision = "once"
		}
		if err := s.json(ctx, "POST", sessionPath(pending.SessionID)+"/permission/"+url.PathEscape(c.ID)+"/reply", map[string]string{"decision": decision}, nil); err != nil {
			return err
		}
	}
	delete(t.pending, c.ID)
	return t.emit(harness.Event{Type: "permission_resolved", Time: time.Now(), Data: harness.JSON(map[string]any{"id": c.ID, "allowed": c.Allow})})
}
