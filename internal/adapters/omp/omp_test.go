package omp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func testDriver(t *testing.T) (*Driver, string, string) {
	t.Helper()
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "project")
	for _, dir := range []string{home, cwd} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	driver, err := New(harness.Config{Adapter: "omp", Env: map[string]string{
		"HOME": home, "OMP_PROFILE": "", "PI_PROFILE": "", "PI_CONFIG_DIR": "", "PI_CODING_AGENT_DIR": "", "PI_CODING_AGENT_SESSION_DIR": "", "XDG_DATA_HOME": "", "TMPDIR": "/tmp",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return driver.(*Driver), home, cwd
}

func writeFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func transcript(id, cwd, title string, slot bool) string {
	var data string
	if slot {
		data = string(harness.JSON(map[string]any{"type": "title", "v": 1, "title": title, "source": "user", "updatedAt": "2026-10-22T00:00:00Z", "pad": ""})) + "\n"
	}
	data += string(harness.JSON(map[string]any{"type": "session", "version": 3, "id": id, "cwd": cwd, "title": "Header title", "timestamp": "2026-10-22T00:00:00Z"})) + "\n"
	data += `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"First prompt"}]}}` + "\n"
	data += `{"type":"model_change","model":"vendor/default-model"}` + "\n"
	data += `{"type":"model_change","model":"vendor/smol-model","role":"smol"}` + "\n"
	data += `{"type":"title_change","title":"Audit title"}` + "\n"
	return data
}

func TestOMPPathsRespectProfilesOverridesAndXDG(t *testing.T) {
	d, home, cwd := testDriver(t)
	check := func(want string, explicit bool) {
		t.Helper()
		got, isExplicit, err := d.sessionsRoot(cwd)
		if err != nil || got != want || isExplicit != explicit {
			t.Fatalf("session root: %s, explicit=%t, %v; want %s, %t", got, isExplicit, err, want, explicit)
		}
	}
	check(filepath.Join(home, ".omp/agent/sessions"), false)
	d.cfg.Env["PI_CODING_AGENT_DIR"] = filepath.Join(home, "override")
	check(filepath.Join(home, "override/sessions"), false)
	d.cfg.Env["OMP_PROFILE"] = "work"
	check(filepath.Join(home, ".omp/profiles/work/agent/sessions"), false)
	d.cfg.Command = []string{"omp", "--profile=team"}
	check(filepath.Join(home, ".omp/profiles/team/agent/sessions"), false)
	d.cfg.Command = []string{"omp"}
	d.cfg.Env["PI_CODING_AGENT_DIR"] = ""
	d.cfg.Env["XDG_DATA_HOME"] = filepath.Join(home, "data")
	check(filepath.Join(home, ".omp/profiles/work/agent/sessions"), false)
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if err := os.MkdirAll(filepath.Join(home, "data/omp/profiles/work"), 0700); err != nil {
			t.Fatal(err)
		}
		check(filepath.Join(home, "data/omp/profiles/work/sessions"), false)
	}
	d.cfg.Env["OMP_PROFILE"], d.cfg.Env["PI_PROFILE"] = "", "legacy"
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		check(filepath.Join(home, "data/omp/sessions"), false)
	} else {
		check(filepath.Join(home, ".omp/agent/sessions"), false)
	} // Explicit empty canonical profile wins over the inherited legacy profile.
	d.cfg.Env["PI_CODING_AGENT_SESSION_DIR"] = "custom"
	check(filepath.Join(cwd, "custom"), true)
	d.cfg.Command = []string{"omp", "--session-dir", filepath.Join(home, "command-store")}
	check(filepath.Join(home, "command-store"), true)
	for _, name := range []string{"../outside", "UPPER", "con", "nul.txt", "work."} {
		if _, err := normalizeProfile(name); err == nil {
			t.Errorf("invalid profile accepted: %q", name)
		}
	}
}

func TestOMPDiscoveryNativeTitleSlotAndOversizedRecords(t *testing.T) {
	d, home, cwd := testDriver(t)
	root := filepath.Join(home, ".omp/agent/sessions/bucket")
	path := filepath.Join(root, "2026_native-id.jsonl")
	data := transcript("native-id", cwd, "Current native title", true)
	data += `{"type":"message","message":{"role":"toolResult","content":"` + strings.Repeat("x", 2<<20) + `"}}` + "\n"
	data += `{"type":"title_change","title":"Older audit cannot override slot"}` + "\n"
	writeFixture(t, path, data)
	writeFixture(t, filepath.Join(root, "subagents/child.jsonl"), transcript("subagent", cwd, "Subagent", true))
	writeFixture(t, filepath.Join(root, "invalid.jsonl"), `{"type":"message"}`)
	got, err := d.Discover(context.Background(), cwd, 10)
	if err != nil || len(got) != 1 || got[0].ID != "native-id" || got[0].Title != "Current native title" || got[0].Model != "vendor/default-model" {
		t.Fatalf("native metadata: %#v, %v", got, err)
	}
	writeFixture(t, path, transcript("native-id", cwd, "", false))
	got, err = d.Discover(context.Background(), cwd, 10)
	if err != nil || len(got) != 1 || got[0].Title != "Audit title" {
		t.Fatalf("legacy header/audit title: %#v, %v", got, err)
	}
}

func TestOMPWorkspaceAndExactLookupBypassUnrelatedHistoryCap(t *testing.T) {
	d, home, cwd := testDriver(t)
	root := filepath.Join(home, ".omp/agent/sessions/bucket")
	for i := 0; i < 502; i++ {
		project := "/other"
		if i == 501 {
			project = cwd
		}
		path := filepath.Join(root, fmt.Sprintf("session-%03d.jsonl", i))
		writeFixture(t, path, transcript(fmt.Sprintf("id-%d", i), project, "Title", true))
		modified := time.Unix(int64(1000-i), 0)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Discover(context.Background(), cwd, 1)
	if err != nil || len(got) != 1 || got[0].ID != "id-501" {
		t.Fatalf("workspace match hidden by unrelated sessions: %#v, %v", got, err)
	}
	got, err = d.Find(context.Background(), "", "id-501")
	if err != nil || len(got) != 1 || got[0].ID != "id-501" {
		t.Fatalf("exact native lookup: %#v, %v", got, err)
	}
}

func TestOMPNewAndResumeArguments(t *testing.T) {
	d, home, cwd := testDriver(t)
	d.cfg.Model = "provider/new-only"
	dir, err := d.projectSessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	args, err := d.InteractiveArgsForCWD(cwd, nil)
	if err != nil || !reflect.DeepEqual(args, []string{"--session-dir", dir, "--model", "provider/new-only"}) {
		t.Fatalf("fresh persistent native launch: %q, %v", args, err)
	}
	path := filepath.Join(home, ".omp/agent/sessions/bucket/id.jsonl")
	writeFixture(t, path, transcript("native-id", cwd, "Native title", true))
	session, err := readDescriptor(path, time.Now(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	args, err = d.InteractiveArgsForCWD(cwd, &session)
	if err != nil || !reflect.DeepEqual(args, []string{"--resume", path}) {
		t.Fatalf("resume must retain native settings and file: %q, %v", args, err)
	}
	session.ID = "wrong-id"
	if _, err := d.InteractiveArgsForCWD(cwd, &session); err == nil {
		t.Fatal("mismatched session ID accepted")
	}
	d.cfg.Command = []string{"omp", "--continue"}
	if _, err := d.InteractiveArgsForCWD(cwd, nil); err == nil {
		t.Fatal("configured auto-continue command defeats new-session intent")
	}
	d.cfg.Command = []string{"omp"}
	args, err = d.InteractiveOptions("max", "", "always-ask")
	if err != nil || !reflect.DeepEqual(args, []string{"--thinking", "max", "--approval-mode", "always-ask"}) {
		t.Fatalf("native options: %q, %v", args, err)
	}
	if _, err := d.InteractiveOptions("", "researcher", ""); !errors.Is(err, harness.ErrUnsupported) {
		t.Fatalf("top-level agent must be rejected: %v", err)
	}
	if _, err := d.InteractiveOptions("", "", "plan"); err == nil {
		t.Fatal("foreign permission mode accepted")
	}
}

func TestOMPCatalogProcess(t *testing.T) {
	if os.Getenv("ATLAS_OMP_CATALOG_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	if !reflect.DeepEqual(os.Args[len(os.Args)-2:], []string{"models", "--json"}) {
		os.Exit(2)
	}
	fmt.Println(`{"models":[{"provider":"vendor","id":"model/x","selector":"vendor/model/x","name":"Native model","contextWindow":8192,"thinking":["off","high"]}]}`)
	os.Exit(0)
}

func TestOMPCatalogAndUnsupportedManagedRun(t *testing.T) {
	d, _, cwd := testDriver(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d.cfg.Command = []string{binary, "-test.run=^TestOMPCatalogProcess$", "--"}
	d.cfg.Env["ATLAS_OMP_CATALOG_HELPER"] = "1"
	catalog, err := d.Catalog(context.Background(), cwd)
	if err != nil || len(catalog.Models) != 1 || catalog.Models[0].ID != "vendor/model/x" || !reflect.DeepEqual(catalog.Models[0].Thinking, []string{"off", "high"}) {
		t.Fatalf("native model catalog: %#v, %v", catalog, err)
	}
	if _, err := d.Run(context.Background(), harness.RunRequest{}, nil, nil); !errors.Is(err, harness.ErrUnsupported) {
		t.Fatalf("unimplemented managed protocol accepted: %v", err)
	}
	if _, err := New(harness.Config{Options: map[string]json.RawMessage{"approve": json.RawMessage("true")}}); err == nil {
		t.Fatal("Pi-only option accepted by OMP")
	}
}
