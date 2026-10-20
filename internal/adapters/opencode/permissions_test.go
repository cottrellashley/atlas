package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

// These are real native child sessions, permissions, forms and replies. No
// provider inference is needed to verify the ownership and routing boundary.
func TestRealOpenCodeDescendantRequests(t *testing.T) {
	if os.Getenv("ATLAS_OPENCODE_REAL") != "1" {
		t.Skip("set ATLAS_OPENCODE_REAL=1 to verify native descendant request routing")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state")}
	drv, err := New(harness.Config{Adapter: "opencode", Env: env})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := drv.(*driver).start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err = s.readyLocation(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	createSession := func(parent string) string {
		t.Helper()
		body := map[string]any{"location": map[string]string{"directory": cwd}, "title": "Atlas isolated request routing test", "permissions": []rule{{Action: "*", Resource: "*", Effect: "ask"}}}
		if parent != "" {
			body["parentID"] = parent
		}
		var response value[nativeSession]
		if err := s.json(ctx, "POST", "/api/session", body, &response); err != nil {
			t.Fatal(err)
		}
		return response.Data.ID
	}
	ownedRoot := createSession("")
	child := createSession(ownedRoot)
	grandchild := createSession(child)
	foreignRoot := createSession("")
	foreignChild := createSession(foreignRoot)
	createPermission := func(id string) string {
		t.Helper()
		var response value[struct {
			ID     string `json:"id"`
			Effect string `json:"effect"`
		}]
		if err := s.json(ctx, "POST", sessionPath(id)+"/permission", map[string]any{"action": "shell", "resources": []string{"printf ATLAS_CHILD_PERMISSION"}}, &response); err != nil {
			t.Fatal(err)
		}
		if response.Data.Effect != "ask" {
			t.Fatalf("native request did not ask: %+v", response)
		}
		return response.Data.ID
	}
	createForm := func(id string) string {
		t.Helper()
		var response value[struct {
			ID string `json:"id"`
		}]
		if err := s.json(ctx, "POST", sessionPath(id)+"/form", map[string]any{"title": "Isolated Atlas question", "fields": []any{map[string]any{"key": "answer", "type": "string", "required": true}}}, &response); err != nil {
			t.Fatal(err)
		}
		return response.Data.ID
	}
	permissionID := createPermission(child)
	foreignPermission := createPermission(foreignChild)
	formID := createForm(grandchild)
	foreignForm := createForm(foreignChild)
	var events []harness.Event
	tr := newTracker(ownedRoot, func(e harness.Event) error { events = append(events, e); return nil })
	tr.cwd = cwd
	if err := tr.permissions(ctx, s); err != nil {
		t.Fatal(err)
	}
	if len(tr.pending) != 2 || len(events) != 2 {
		t.Fatalf("root descendants not isolated: pending=%+v events=%+v", tr.pending, events)
	}
	if tr.pending[permissionID].SessionID != child || tr.pending[formID].SessionID != grandchild {
		t.Fatal("wrong owning sessions for child requests")
	}
	for _, id := range []string{foreignPermission, foreignForm} {
		if err := tr.control(ctx, s, harness.Control{Type: "permission", ID: id, Allow: true, Text: `{"answer":"wrong owner"}`}); err == nil {
			t.Fatal("accepted another root session's request")
		}
	}
	if err := tr.control(ctx, s, harness.Control{Type: "permission", ID: permissionID, Allow: true}); err != nil {
		t.Fatal(err)
	}
	if err := tr.control(ctx, s, harness.Control{Type: "permission", ID: formID, Allow: true, Text: `{"answer":"Atlas child routing works"}`}); err != nil {
		t.Fatal(err)
	}
	if len(tr.pending) != 0 {
		t.Fatalf("answered descendant requests remained pending: %+v", tr.pending)
	}
	var permissions page[json.RawMessage]
	if err := s.json(ctx, "GET", sessionPath(foreignChild)+"/permission", nil, &permissions); err != nil {
		t.Fatal(err)
	}
	var forms page[json.RawMessage]
	if err := s.json(ctx, "GET", sessionPath(foreignChild)+"/form", nil, &forms); err != nil {
		t.Fatal(err)
	}
	if len(permissions.Data) != 1 || len(forms.Data) != 1 {
		t.Fatal("foreign pending requests were changed")
	}
	if err := tr.permissions(ctx, s); err != nil {
		t.Fatal(err)
	}
	if len(tr.pending) != 0 || len(events) != 4 {
		t.Fatalf("poll replayed resolved/foreign requests: pending=%+v events=%+v", tr.pending, events)
	}
	t.Log("real native child permission + grandchild question replied through owning endpoints; sibling root and child requests untouched")
}
