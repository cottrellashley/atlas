// Package harness defines the transport-independent contract. Model IDs and
// persistence handles are opaque outside the adapter that produced them.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrUnsupported = errors.New("operation is not supported by this harness")

type Config struct {
	ID      string                     `json:"id,omitempty"`
	Adapter string                     `json:"adapter"`
	Command []string                   `json:"command"`
	Env     map[string]string          `json:"env,omitempty"`
	Options map[string]json.RawMessage `json:"options,omitempty"`
	Model   string                     `json:"model,omitempty"`
}
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Default       bool     `json:"default,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	Thinking      []string `json:"thinking,omitempty"`
}
type Mode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Catalog struct {
	Models []Model `json:"models"`
	Modes  []Mode  `json:"modes,omitempty"`
	Source string  `json:"source,omitempty"`
}
type Handle struct {
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}
type NativeSession struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CWD       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updatedAt"`
	Handle    Handle    `json:"handle"`
	Model     string    `json:"model,omitempty"`
}
type RunRequest struct {
	SessionID      string  `json:"sessionId"`
	CWD            string  `json:"cwd"`
	Prompt         string  `json:"prompt"`
	Model          string  `json:"model,omitempty"`
	Thinking       string  `json:"thinking,omitempty"`
	NativeAgent    string  `json:"nativeAgent,omitempty"`
	PermissionMode string  `json:"permissionMode,omitempty"`
	Resume         *Handle `json:"resume,omitempty"`
	StateDir       string  `json:"stateDir"`
}
type RunResult struct {
	Handle *Handle `json:"handle,omitempty"`
	Model  string  `json:"model,omitempty"`
	Text   string  `json:"text,omitempty"`
}

// Event kinds: text, reasoning, tool, permission, permission_resolved,
// persistence, config, usage, notice. The manager owns turn terminal events.
// A persistence event must be emitted as soon as the native identity is known.
type Event struct {
	Seq        int64           `json:"seq,omitempty"`
	Time       time.Time       `json:"time"`
	SessionID  string          `json:"sessionId,omitempty"`
	TurnID     string          `json:"turnId,omitempty"`
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Handle     *Handle         `json:"handle,omitempty"`
	Model      string          `json:"model,omitempty"`
	Permission *Permission     `json:"permission,omitempty"`
}
type Permission struct {
	ID          string          `json:"id"`
	Tool        string          `json:"tool"`
	Description string          `json:"description,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Choices     []string        `json:"choices,omitempty"`
}
type Control struct {
	Type  string `json:"type"` // permission or steer (where supported)
	ID    string `json:"id,omitempty"`
	Allow bool   `json:"allow,omitempty"`
	Text  string `json:"text,omitempty"`
}
type Emit func(Event) error

// Run owns one turn and all its child processes; cancellation must settle and
// reap them before returning. Return nil only after native completion, never
// after mere prompt acceptance. Failed runs still return any known handle.
type Driver interface {
	Catalog(context.Context, string) (Catalog, error)
	Discover(context.Context, string, int) ([]NativeSession, error)
	Run(context.Context, RunRequest, Emit, <-chan Control) (RunResult, error)
}

func JSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
