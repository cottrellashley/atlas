package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
	"github.com/cottrellashley/atlas/internal/store"
)

// Runs a freshly built CLI, its real background daemon, and the actual installed
// OpenCode subprocess. The caller supplies a deterministic local LLM endpoint.
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
	defer func() {
		// This affects only the freshly created test ATLAS_HOME.
		if _, err := run("daemon", "stop"); err != nil {
			t.Errorf("test daemon cleanup failed: %v", err)
		}
	}()
	decodeRecord := func(out []byte) store.Record {
		t.Helper()
		var r store.Record
		if err := json.Unmarshal(out, &r); err != nil || r.ID == "" {
			t.Fatalf("invalid record: %s (%v)", out, err)
		}
		return r
	}
	show := func(id string) store.Record { return decodeRecord(must("sessions", "show", id)) }
	wait := func(id string) store.Record {
		t.Helper()
		r := decodeRecord(must("wait", id, "--timeout", "40s", "--json"))
		if r.State != "idle" || r.Resume == nil {
			t.Fatalf("completed session state/handle: %+v", r)
		}
		return r
	}
	must("init", "--harness", "opencode", "--template", "researcher", "--cwd", cwd)
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
	first := decodeRecord(must("run", "--background", "--json", "--model", "atlasfixture/echo", "--permission-mode", "deny", "Reply ATLAS_FIXTURE_OK."))
	first = wait(first.ID)
	logs := must("logs", first.ID, "--json")
	dec := json.NewDecoder(bytes.NewReader(logs))
	lastSeq := int64(0)
	var text strings.Builder
	persisted, completed := false, false
	for {
		var e harness.Event
		err := dec.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil || e.Seq <= lastSeq {
			t.Fatalf("invalid event sequence: %v %s", err, logs)
		}
		lastSeq = e.Seq
		if e.Type == "text" {
			text.WriteString(e.Text)
		}
		persisted = persisted || e.Type == "persistence"
		completed = completed || e.Type == "turn.completed"
	}
	if text.String() != "ATLAS_FIXTURE_OK" || !persisted || !completed {
		t.Fatalf("CLI event replay: text=%q persistence=%v completed=%v events=%s", text.String(), persisted, completed, logs)
	}
	if got := must("logs", first.ID, "--after", jsonNumber(lastSeq), "--json"); len(bytes.TrimSpace(got)) != 0 {
		t.Fatalf("log cursor replayed events: %s", got)
	}
	must("daemon", "stop")
	resumed := decodeRecord(must("send", first.ID, "Repeat your previous response.", "--background", "--json"))
	resumed = wait(resumed.ID)
	if string(resumed.Resume.Data) != string(first.Resume.Data) {
		t.Fatal("daemon restart/send changed native session identity")
	}
	var discovered []harness.NativeSession
	if err := json.Unmarshal(must("sessions", "discover", "--json"), &discovered); err != nil || len(discovered) != 1 {
		t.Fatalf("native discover: %+v %v", discovered, err)
	}
	imported := decodeRecord(must("sessions", "import", "--handle", externalID, "--cwd", externalCWD, "--harness", "opencode", "--json"))
	if imported.ID == first.ID || imported.Resume == nil {
		t.Fatal("native-only session import did not create an independent Atlas record")
	}
	importedAgain := decodeRecord(must("sessions", "import", "--handle", externalID, "--cwd", externalCWD, "--harness", "opencode", "--json"))
	if importedAgain.ID != imported.ID {
		t.Fatal("import was not idempotent")
	}
	must("send", imported.ID, "Repeat your previous response.", "--background")
	wait(imported.ID)
	awaitPermission := func(id string) harness.Permission {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var permissions []harness.Permission
			if err := json.Unmarshal(must("permissions", "list", id), &permissions); err != nil {
				t.Fatal(err)
			}
			if len(permissions) != 0 {
				return permissions[0]
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("permission did not appear: %+v", show(id))
		return harness.Permission{}
	}
	denied := decodeRecord(must("run", "--background", "--json", "--permission-mode", "ask", "--model", "atlasfixture/echo", "ATLAS_TOOL_SCENARIO"))
	permission := awaitPermission(denied.ID)
	must("permissions", "deny", denied.ID, permission.ID)
	deniedOutput, deniedError := run("wait", denied.ID, "--timeout", "40s", "--json")
	deniedResult := decodeRecord(deniedOutput)
	if deniedError == nil || deniedResult.Resume == nil || !strings.Contains(deniedResult.Error, "interrupted") {
		t.Fatalf("native permission denial must terminate and preserve identity: %+v error=%v", deniedResult, deniedError)
	}
	if _, err := os.Stat(filepath.Join(cwd, "atlas-tool.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied native shell unexpectedly created marker: %v", err)
	}
	approved := decodeRecord(must("run", "--background", "--json", "--permission-mode", "ask", "--model", "atlasfixture/echo", "ATLAS_TOOL_SCENARIO"))
	permission = awaitPermission(approved.ID)
	must("permissions", "allow", approved.ID, permission.ID)
	wait(approved.ID)
	marker, err := os.ReadFile(filepath.Join(cwd, "atlas-tool.txt"))
	if err != nil || string(marker) != "ATLAS_TOOL_OK" {
		t.Fatalf("approved native shell failed: %q %v", marker, err)
	}
	canceled := decodeRecord(must("run", "--background", "--json", "--permission-mode", "deny", "--model", "atlasfixture/echo", "ATLAS_CANCEL_SCENARIO"))
	deadline := time.Now().Add(30 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		if bytes.Contains(must("logs", canceled.ID), []byte("ATLAS_STREAM_STARTED")) {
			started = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !started {
		t.Fatal("cancel test never reached active model stream")
	}
	must("stop", canceled.ID)
	stopped := show(canceled.ID)
	if stopped.State != "stopped" || stopped.Resume == nil {
		t.Fatalf("stop lost persisted session or did not settle: %+v", stopped)
	}
	var listed []store.Record
	if err := json.Unmarshal(must("sessions", "list", "--all", "--json"), &listed); err != nil || len(listed) != 5 {
		t.Fatalf("list records=%d err=%v", len(listed), err)
	}
	t.Logf("fresh CLI passed init, models, background run, logs/cursor, wait, restart/send, discover/import/resume, permissions deny/allow, active cancellation, list (%d sessions)", len(listed))
}

func jsonNumber(n int64) string { return string(harness.JSON(n)) }
