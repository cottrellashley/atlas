package claude

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
	project := filepath.Join(root, "projects", "native-project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_600_000_000, 0)
	var wantedID string
	for i := 0; i < 502; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		cwd := "/work/other"
		if i == 501 {
			wantedID, cwd = id, "/work/research"
		}
		path := filepath.Join(project, id+".jsonl")
		data := harness.JSON(map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "Research"}})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		modified := old.Add(time.Duration(502-i) * time.Second)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	d := &driver{cfg: harness.Config{Env: map[string]string{"CLAUDE_CONFIG_DIR": root}}}
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

func TestSummaryKeepsSessionWithOversizedTranscriptRecords(t *testing.T) {
	for _, tail := range []bool{false, true} {
		t.Run(fmt.Sprintf("tail-metadata-%t", tail), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "00000000-0000-4000-8000-000000000001.jsonl")
			data := `{"type":"user","cwd":"/work/research","message":{"content":"First prompt"}}` + "\n"
			data += `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 2<<20) + `"}}` + "\n"
			want := "First prompt"
			if tail {
				data += `{"type":"custom-title","customTitle":"Latest title"}` + "\n"
				want = "Latest title"
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			session, err := readSummary(path, time.Now())
			if err != nil || session.CWD != "/work/research" || session.Title != want || session.Handle.Version != 1 {
				t.Fatalf("oversized tool record discarded native session: %#v, %v", session, err)
			}
		})
	}
}

func TestSummaryTailIncludesRecordExactlyAtWindowBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00000000-0000-4000-8000-000000000001.jsonl")
	head := `{"type":"user","cwd":"/work/research","message":{"content":"First prompt"}}` + "\n"
	head += strings.Repeat("x", 300<<10) + "\n"
	tail := `{"type":"custom-title","customTitle":"Boundary title"}` + "\n"
	tail += strings.Repeat(" ", (256<<10)-len(tail)-1) + "\n"
	if err := os.WriteFile(path, []byte(head+tail), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := readSummary(path, time.Now())
	if err != nil || session.Title != "Boundary title" {
		t.Fatalf("tail boundary skipped a complete metadata record: %#v, %v", session, err)
	}
}
