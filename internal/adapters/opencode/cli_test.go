package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
)

// Runs a freshly built CLI against the installed OpenCode history. The caller
// has already created native sessions using a deterministic local model endpoint.
func testOpenCodeCLI(t *testing.T, env map[string]string, root, externalID, externalCWD string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	module := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	bin := filepath.Join(root, "atlas")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "./cmd/atlas")
	build.Dir = module
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	home := filepath.Join(root, "atlas-home")
	cwd := filepath.Join(root, "cli-project")
	for _, p := range []string{home, cwd} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := harness.Config{Adapter: "opencode", Command: []string{"opencode"}, Env: env}
	configuration := map[string]any{"schemaVersion": 1, "harnesses": map[string]any{"opencode": profile}}
	if err := os.WriteFile(filepath.Join(home, "config.json"), harness.JSON(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = cwd
		cmd.Env = process.Environment(map[string]string{"ATLAS_HOME": home})
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Logf("CLI %v: %v; stderr=%s", args, err, stderr.String())
		}
		return out, err
	}
	must := func(args ...string) []byte {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	must("init", "researcher", "--harness", "opencode", "--cwd", cwd, "--no-open")
	for _, p := range []string{"AGENTS.md", "opencode.json", ".atlas/project.json", ".opencode/skills/research/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(cwd, p)); err != nil {
			t.Fatalf("init expected native file %s: %v", p, err)
		}
	}
	var catalog harness.Catalog
	if err := json.Unmarshal(must("models", "--json"), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range catalog.Models {
		found = found || m.ID == "atlasfixture/echo"
	}
	if !found {
		t.Fatal("CLI model catalog lacks fixture provider")
	}
	type group struct {
		Harness  string                  `json:"harness"`
		Sessions []harness.NativeSession `json:"sessions"`
		Error    string                  `json:"error"`
	}
	list := func(args ...string) []harness.NativeSession {
		t.Helper()
		var groups []group
		argv := append([]string{"session", "list", "--harness", "opencode", "--json"}, args...)
		out := must(argv...)
		if err := json.Unmarshal(out, &groups); err != nil || len(groups) != 1 || groups[0].Harness != "opencode" || groups[0].Error != "" {
			t.Fatalf("native groups: %s %v", out, err)
		}
		return groups[0].Sessions
	}
	all := list()
	found = false
	for _, session := range all {
		if session.ID == externalID {
			found = true
			if session.CWD != externalCWD {
				t.Fatalf("global discovery changed native project: %#v", session)
			}
			decoded, err := decodeHandle(session.Handle)
			if err != nil || decoded.SessionID != externalID || decoded.CWD != externalCWD {
				t.Fatalf("global discovery changed native handle: %#v %v", decoded, err)
			}
		}
	}
	if !found {
		t.Fatalf("global history omitted external native conversation %s: %#v", externalID, all)
	}
	if sessions := list("--cwd", cwd); len(sessions) != 0 {
		t.Fatalf("fresh project unexpectedly contains native sessions: %#v", sessions)
	}
	filtered := list("--cwd", externalCWD, "--search", externalID)
	if len(filtered) != 1 || filtered[0].ID != externalID {
		t.Fatalf("native project/search filters: %#v", filtered)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("native browsing created Atlas session records: %v", err)
	}
	t.Logf("native CLI passed init --no-open, models, global history, native handles, project filters and search (%d native sessions)", len(all))
}
