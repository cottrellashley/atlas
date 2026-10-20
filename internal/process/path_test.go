package process

import (
	"context"
	"github.com/cottrellashley/atlas/internal/harness"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredPathResolvesExecutable(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "atlas-test-binary"), []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	cfg := harness.Config{Command: []string{"atlas-test-binary"}, Env: map[string]string{"PATH": dir}}
	out, e := Command(context.Background(), cfg, t.TempDir(), "literal;$HOME").Output()
	if e != nil || string(out) != "literal;$HOME" {
		t.Fatalf("%s %v", out, e)
	}
}
func TestParentHarnessMarkersAreStripped(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SSE_PORT", "1234")
	env := Environment(nil)
	for _, v := range env {
		if strings.HasPrefix(v, "CLAUDECODE=") || strings.HasPrefix(v, "CLAUDE_CODE_SSE_PORT=") {
			t.Fatalf("marker leaked: %s", v)
		}
	}
}
