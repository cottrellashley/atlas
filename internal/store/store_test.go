package store

import (
	"github.com/cottrellashley/atlas/internal/harness"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverTruncatedTail(t *testing.T) {
	s := Store{Home: t.TempDir()}
	id := ID()
	if e := s.Append(id, harness.Event{Seq: 1, Type: "persistence", Handle: &harness.Handle{Version: 1, Data: harness.JSON(map[string]string{"id": "native"})}}); e != nil {
		t.Fatal(e)
	}
	dir, _ := s.Dir(id)
	f, e := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = f.WriteString(`{"seq":2,"type":`)
	_ = f.Close()
	events, e := s.RecoverEvents(id)
	if e != nil || len(events) != 1 {
		t.Fatal(events, e)
	}
	if e = s.Append(id, harness.Event{Seq: 2, Type: "turn.interrupted"}); e != nil {
		t.Fatal(e)
	}
	events, e = s.Events(id, 0, 0)
	if e != nil || len(events) != 2 {
		t.Fatal(events, e)
	}
}
func TestRefuseCorruptCompleteRecord(t *testing.T) {
	s := Store{Home: t.TempDir()}
	id := ID()
	dir, _ := s.Dir(id)
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("not JSON\n"), 0600)
	if _, e := s.RecoverEvents(id); e == nil {
		t.Fatal("silently skipped corruption")
	}
}
func TestIDTraversalRejected(t *testing.T) {
	if _, e := (Store{Home: t.TempDir()}).Dir("../../elsewhere"); e == nil {
		t.Fatal("accepted path traversal")
	}
}
