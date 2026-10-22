package pi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func TestDiscoveryFiltersWorkspaceBeforeResultLimit(t *testing.T) {
	root := t.TempDir()
	old := time.Unix(1_600_000_000, 0)
	var wantedID string
	for i := 0; i < 502; i++ {
		id := fmt.Sprintf("native-%d", i)
		cwd := "/work/other"
		if i == 501 {
			wantedID, cwd = id, "/work/research"
		}
		path := filepath.Join(root, fmt.Sprintf("2026-10-21_%s.jsonl", id))
		data := harness.JSON(map[string]any{"type": "session", "id": id, "cwd": cwd})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		modified := old.Add(time.Duration(502-i) * time.Second)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	d := &Driver{opts: options{SessionDir: root}}
	got, err := d.Discover(context.Background(), "/work/research", 1)
	if err != nil || len(got) != 1 || got[0].ID != wantedID {
		t.Fatalf("older workspace session hidden by unrelated histories: %#v, %v", got, err)
	}
	got, err = d.Find(context.Background(), "", wantedID)
	if err != nil || len(got) != 1 || got[0].ID != wantedID {
		t.Fatalf("exact ID lookup must bypass recent history limit: %#v, %v", got, err)
	}
	got, err = d.Find(context.Background(), "/work/other", wantedID)
	if err != nil || len(got) != 0 {
		t.Fatalf("exact ID lookup ignored cwd: %#v, %v", got, err)
	}
}

func TestDescriptorKeepsSessionWithOversizedTranscriptRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := `{"type":"session","id":"native-id","cwd":"/work/research"}` + "\n"
	data += `{"type":"message","message":{"role":"user","content":"First prompt"}}` + "\n"
	data += `{"type":"message","message":{"role":"toolResult","content":"` + strings.Repeat("x", 2<<20) + `"}}` + "\n"
	data += `{"type":"session_info","name":"Latest title"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := readSessionDescriptor(path, time.Now())
	if err != nil || session.CWD != "/work/research" || session.Title != "Latest title" || session.Handle.Version != 1 {
		t.Fatalf("oversized tool record discarded native session: %#v, %v", session, err)
	}
}

func TestDescriptorTailIncludesRecordExactlyAtWindowBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	head := `{"type":"session","id":"native-id","cwd":"/work/research"}` + "\n"
	head += strings.Repeat("x", 300<<10) + "\n"
	tail := `{"type":"session_info","name":"Boundary title"}` + "\n"
	tail += strings.Repeat(" ", (256<<10)-len(tail)-1) + "\n"
	if err := os.WriteFile(path, []byte(head+tail), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := readSessionDescriptor(path, time.Now())
	if err != nil || session.Title != "Boundary title" {
		t.Fatalf("tail boundary skipped a complete metadata record: %#v, %v", session, err)
	}
}
