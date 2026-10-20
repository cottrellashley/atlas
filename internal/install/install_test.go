package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, path, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(content), mode); e != nil {
		t.Fatal(e)
	}
}
func get(t *testing.T, root, path string) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(root, path))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func apply(t *testing.T, root string, cs ...Contribution) *Transaction {
	t.Helper()
	p, e := Plan(root, cs)
	if e != nil {
		t.Fatal(e)
	}
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	return p
}
func missing(t *testing.T, root, path string) {
	t.Helper()
	if _, e := os.Stat(filepath.Join(root, path)); !os.IsNotExist(e) {
		t.Fatalf("expected %s missing, got %v", path, e)
	}
}
func value(t *testing.T, root, path, pointer string) json.RawMessage {
	t.Helper()
	d, e := parseDocument([]byte(get(t, root, path)))
	if e != nil {
		t.Fatal(e)
	}
	v, ok, e := d.get(pointer)
	if e != nil || !ok {
		t.Fatalf("get %s: exists %v, error %v", pointer, ok, e)
	}
	return v
}

func TestLifecyclePreservesUnrelatedJSONCAndExecutableMode(t *testing.T) {
	root := t.TempDir()
	original := "{\n // user's setting\n \"theme\": \"dark\", // keep this\n}\n"
	put(t, root, "opencode.jsonc", original, 0640)
	c := Contribution{PackageID: "@team/research", Files: []File{{Path: ".opencode/skills/research/SKILL.md", Data: []byte("skill one"), Mode: 0644}, {Path: ".opencode/skills/research/run.sh", Data: []byte("#!/bin/sh\ntrue\n"), Mode: 0755}}, Values: []Value{{Path: "opencode.jsonc", Pointer: "/mcp/docs/command", Value: []string{"docs-server"}}, {Path: "opencode.jsonc", Pointer: "/plugins", Value: "research@1.0", Array: true}}}
	p, e := Plan(root, []Contribution{c})
	if e != nil {
		t.Fatal(e)
	}
	missing(t, root, receiptPath)
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	if got := get(t, root, "opencode.jsonc"); !strings.Contains(got, "// user's setting") || !strings.Contains(got, "// keep this") {
		t.Fatalf("comments lost: %s", got)
	}
	info, e := os.Stat(filepath.Join(root, ".opencode/skills/research/run.sh"))
	if e != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode: %v %v", info, e)
	}
	info, e = os.Stat(filepath.Join(root, "opencode.jsonc"))
	if e != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("config mode: %v %v", info, e)
	}
	// A user adds independent configuration after installation.
	d, e := parseDocument([]byte(get(t, root, "opencode.jsonc")))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.set("/userAdded", []byte("42")); e != nil {
		t.Fatal(e)
	}
	put(t, root, "opencode.jsonc", string(d.data), 0640)
	c.Files = c.Files[1:]
	c.Values[0].Value = []string{"docs-server", "--v2"}
	c.Values[1].Value = "research@2.0"
	apply(t, root, c)
	missing(t, root, ".opencode/skills/research/SKILL.md")
	if !jsonEqual(value(t, root, "opencode.jsonc", "/plugins"), []byte(`["research@2.0"]`)) {
		t.Fatal("array update did not reconcile old entry")
	}
	apply(t, root)
	missing(t, root, ".opencode/skills/research/run.sh")
	final := get(t, root, "opencode.jsonc")
	if !strings.Contains(final, "// keep this") || !jsonEqual(value(t, root, "opencode.jsonc", "/userAdded"), []byte("42")) {
		t.Fatalf("user changes lost: %s", final)
	}
	d, e = parseDocument([]byte(final))
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"/mcp", "/plugins"} {
		_, exists, e := d.get(key)
		if e != nil || exists {
			t.Fatalf("created config container remains at %s: %s", key, final)
		}
	}
}

func TestSharedOwnersAndPreexistingResources(t *testing.T) {
	root := t.TempDir()
	put(t, root, "settings.json", `{"plugins":["preexisting"],"same":true}`, 0600)
	put(t, root, "existing.txt", "same", 0640)
	a := Contribution{PackageID: "a", Files: []File{{Path: "existing.txt", Data: []byte("same")}}, Values: []Value{{Path: "settings.json", Pointer: "/same", Value: true}, {Path: "settings.json", Pointer: "/plugins", Value: "preexisting", Array: true}, {Path: "settings.json", Pointer: "/plugins", Value: "shared", Array: true}}}
	b := Contribution{PackageID: "b", Values: []Value{{Path: "settings.json", Pointer: "/plugins", Value: "shared", Array: true}}}
	apply(t, root, a, b)
	apply(t, root, b)
	if got := value(t, root, "settings.json", "/plugins"); !jsonEqual(got, []byte(`["preexisting","shared"]`)) {
		t.Fatalf("shared dependency removed: %s", got)
	}
	apply(t, root)
	if got := value(t, root, "settings.json", "/plugins"); !jsonEqual(got, []byte(`["preexisting"]`)) {
		t.Fatalf("preexisting item lost: %s", got)
	}
	if get(t, root, "existing.txt") != "same" {
		t.Fatal("preexisting file lost")
	}
	if !jsonEqual(value(t, root, "settings.json", "/same"), []byte("true")) {
		t.Fatal("preexisting value lost")
	}
}

func TestNewConfigurationRemovedOnlyWhenEmpty(t *testing.T) {
	for _, userEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "user-data"}[userEdit], func(t *testing.T) {
			root := t.TempDir()
			c := Contribution{PackageID: "a", Values: []Value{{Path: "settings.json", Pointer: "/a/b", Value: "x"}}}
			apply(t, root, c)
			if userEdit {
				d, _ := parseDocument([]byte(get(t, root, "settings.json")))
				_, _ = d.set("/a/user", []byte("true"))
				put(t, root, "settings.json", string(d.data), 0600)
			}
			apply(t, root)
			if userEdit {
				value(t, root, "settings.json", "/a/user")
			} else {
				missing(t, root, "settings.json")
			}
		})
	}
}

func TestDriftBlocksUpdateAndRemoval(t *testing.T) {
	for _, kind := range []string{"file", "value", "array"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			c := Contribution{PackageID: "a"}
			switch kind {
			case "file":
				c.Files = []File{{Path: "resource", Data: []byte("original")}}
			default:
				c.Values = []Value{{Path: "resource", Pointer: "/owned", Value: "original", Array: kind == "array"}}
			}
			apply(t, root, c)
			if kind == "file" {
				put(t, root, "resource", "edited", 0644)
			} else {
				put(t, root, "resource", `{"owned":"edited"}`, 0600)
			}
			drift, e := Status(root)
			if e != nil || len(drift) != 1 {
				t.Fatalf("drift: %#v %v", drift, e)
			}
			if _, e = Plan(root, nil); e == nil {
				t.Fatal("removal overwrites edits")
			}
			if _, e = Plan(root, []Contribution{c}); e == nil {
				t.Fatal("update overwrites edits")
			}
		})
	}
}

func TestPlanStalenessAndMetadataTransaction(t *testing.T) {
	root := t.TempDir()
	c := Contribution{PackageID: "a", Files: []File{{Path: "resource", Data: []byte("new")}}}
	p, e := Plan(root, []Contribution{c}, File{Path: ".atlas/packages.json", Data: []byte("{}\n")})
	if e != nil {
		t.Fatal(e)
	}
	put(t, root, "resource", "user edit", 0644)
	if e = Apply(root, p); e == nil || !strings.Contains(e.Error(), "stale") {
		t.Fatalf("expected stale plan, got %v", e)
	}
	missing(t, root, receiptPath)
	missing(t, root, ".atlas/packages.json")
	if e = os.Remove(filepath.Join(root, "resource")); e != nil {
		t.Fatal(e)
	}
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	if get(t, root, ".atlas/packages.json") != "{}\n" {
		t.Fatal("metadata missing")
	}
	if e = Apply(root, p); e == nil {
		t.Fatal("reusing an already applied plan should be stale")
	}
}

func TestPlanInputCannotChangeAfterPreview(t *testing.T) {
	root := t.TempDir()
	data := []byte("original")
	p, e := Plan(root, []Contribution{{PackageID: "a", Files: []File{{Path: "resource", Data: data}}}})
	if e != nil {
		t.Fatal(e)
	}
	copy(data, []byte("mutated!"))
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	if get(t, root, "resource") != "original" {
		t.Fatal("caller mutated planned bytes")
	}
}

func TestRejectConflictsAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	put(t, root, "existing", "user", 0600)
	put(t, root, "settings.json", `{"a":3}`, 0600)
	cases := []Contribution{
		{PackageID: "x", Files: []File{{Path: "../escape", Data: []byte("x")}}},
		{PackageID: "x", Files: []File{{Path: receiptPath, Data: []byte("x")}}},
		{PackageID: "x", Files: []File{{Path: "existing", Data: []byte("x")}}},
		{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/a", Value: 4}}},
		{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/a/b", Value: 4}}},
		{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/a", Value: 4, Array: true}}},
		{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/bad~2", Value: 4}}},
		{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/nested", Value: map[string]any{}}, {Path: "settings.json", Pointer: "/nested/child", Value: 4}}},
	}
	for i, c := range cases {
		if _, e := Plan(root, []Contribution{c}); e == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := Plan(root, []Contribution{{PackageID: "x", Files: []File{{Path: "link/escape", Data: []byte("x")}}}}); e == nil {
		t.Fatal("symlink path accepted")
	}
	if _, e := Plan(root, []Contribution{{PackageID: "x", Files: []File{{Path: "shared", Data: []byte("a")}}}, {PackageID: "y", Files: []File{{Path: "shared", Data: []byte("b")}}}}); e == nil {
		t.Fatal("disagreeing owners accepted")
	}
}

func TestRecoveryJournal(t *testing.T) {
	for _, state := range []string{"partial", "committed", "external-edit"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "before", "old", 0640)
			p, e := Plan(root, []Contribution{{PackageID: "x", Files: []File{{Path: "created", Data: []byte("new")}}}}, File{Path: "before", Data: []byte("new")})
			if e != nil {
				t.Fatal(e)
			}
			j := journal{SchemaVersion: 1, Committed: state == "committed", Entries: p.entries}
			if e = write(root, journalPath, snapshot{true, encode(j), 0600}); e != nil {
				t.Fatal(e)
			}
			for _, en := range p.entries {
				if e = write(root, en.Path, en.After); e != nil {
					t.Fatal(e)
				}
			}
			if state == "external-edit" {
				put(t, root, "created", "external", 0644)
				if e = Recover(root); e == nil {
					t.Fatal("recovery overwrote external edit")
				}
				if get(t, root, "created") != "external" {
					t.Fatal("external edit lost")
				}
				return
			}
			if e = Recover(root); e != nil {
				t.Fatal(e)
			}
			missing(t, root, journalPath)
			if state == "partial" {
				missing(t, root, "created")
				missing(t, root, receiptPath)
				if get(t, root, "before") != "old" {
					t.Fatal("original not restored")
				}
			} else {
				if get(t, root, "created") != "new" {
					t.Fatal("committed transaction rolled back")
				}
			}
		})
	}
}

func TestLockAndReceiptSchema(t *testing.T) {
	root := t.TempDir()
	p, e := Plan(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := lock(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = Apply(root, p); e == nil {
		t.Fatal("concurrent install acquired lock")
	}
	unlock()
	put(t, root, receiptPath, `{"schemaVersion":999}`, 0600)
	if _, e = Contributions(root); e == nil {
		t.Fatal("unknown receipt schema accepted")
	}
}

func TestIdempotenceAndReceiptLargeNumbers(t *testing.T) {
	root := t.TempDir()
	c := Contribution{PackageID: "a", Values: []Value{{Path: "settings.json", Pointer: "/number", Value: json.Number("9007199254740993")}}}
	apply(t, root, c)
	cs, e := Contributions(root)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Plan(root, cs)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 0 {
		b, _ := json.Marshal(p)
		t.Fatalf("repeat changes state: %s", b)
	}
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(value(t, root, "settings.json", "/number"), []byte("9007199254740993")) {
		t.Fatal("large number lost precision")
	}
}

func TestJSONCParserRejectsAmbiguityAndKeepsNeighborComments(t *testing.T) {
	for _, input := range []string{`{"a":1,"a":2}`, `{"a":truejunk}`, `{"a":1} trailing`, `{"a":/* never ends}`, `[]`, `null`} {
		if _, e := parseDocument([]byte(input)); e == nil {
			t.Errorf("accepted invalid config %q", input)
		}
	}
	for _, input := range []string{"{\"a\": 1, /* middle */ \"b\": 2, /* tail */}", "{\"a\": 1, /* middle */ \"b\": 2 /* tail */}"} {
		d, e := parseDocument([]byte(input))
		if e != nil {
			t.Fatal(e)
		}
		if e = d.remove("/b"); e != nil {
			t.Fatal(e)
		}
		if _, e = d.set("/c", []byte("3")); e != nil {
			t.Fatal(e)
		}
		if _, e = parseDocument(d.data); e != nil {
			t.Fatalf("invalid edited JSONC: %s: %v", d.data, e)
		}
		if !bytes.Contains(d.data, []byte("/* middle */")) || !bytes.Contains(d.data, []byte("/* tail */")) {
			t.Fatalf("neighbor comments lost: %s", d.data)
		}
	}
}

func TestEmptyOwnedValueSurvivesParentReconciliation(t *testing.T) {
	root := t.TempDir()
	apply(t, root, Contribution{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/parent/child", Value: true}}})
	apply(t, root, Contribution{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/parent", Value: map[string]any{}}}})
	if !jsonEqual(value(t, root, "settings.json", "/parent"), []byte("{}")) {
		t.Fatal("empty owned object was pruned")
	}
}

func TestPlanWithBaseAtomicInitializationAndUninstallBaseline(t *testing.T) {
	root := t.TempDir()
	base := []File{{Path: "AGENTS.md", Data: []byte("project instructions"), Mode: 0644}, {Path: "settings.jsonc", Data: []byte("{\n// template comment\n\"template\":true\n}\n"), Mode: 0640}}
	c := Contribution{PackageID: "research", Values: []Value{{Path: "settings.jsonc", Pointer: "/mcp/docs/command", Value: "docs"}}}
	p, e := PlanWithBase(root, base, []Contribution{c}, File{Path: ".atlas/project.json", Data: []byte("{}")})
	if e != nil {
		t.Fatal(e)
	}
	missing(t, root, "AGENTS.md")
	missing(t, root, "settings.jsonc")
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	if get(t, root, "AGENTS.md") != "project instructions" {
		t.Fatal("unowned base file was not written")
	}
	value(t, root, "settings.jsonc", "/mcp/docs/command")
	apply(t, root)
	if !jsonEqual(value(t, root, "settings.jsonc", "/template"), []byte("true")) {
		t.Fatal("uninstall lost template baseline")
	}
	if !strings.Contains(get(t, root, "settings.jsonc"), "// template comment") {
		t.Fatal("template comment lost")
	}
}

func TestPlanWithBaseConflictLeavesNoPartialTemplate(t *testing.T) {
	root := t.TempDir()
	base := []File{{Path: "AGENTS.md", Data: []byte("instructions")}, {Path: "settings.json", Data: []byte(`{"owned":"template"}`)}}
	c := Contribution{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/owned", Value: "conflicting-package"}}}
	if _, e := PlanWithBase(root, base, []Contribution{c}); e == nil {
		t.Fatal("template/package conflict accepted")
	}
	missing(t, root, "AGENTS.md")
	missing(t, root, "settings.json")
	missing(t, root, receiptPath)
}

func TestPlanWithBaseChecksActualFilesystemStaleness(t *testing.T) {
	root := t.TempDir()
	put(t, root, "settings.json", `{"user":true}`, 0640)
	p, e := PlanWithBase(root, []File{{Path: "settings.json", Data: []byte(`{"user":true,"template":true}`)}}, []Contribution{{PackageID: "x", Values: []Value{{Path: "settings.json", Pointer: "/package", Value: true}}}})
	if e != nil {
		t.Fatal(e)
	}
	put(t, root, "settings.json", `{"user":"edited"}`, 0640)
	if e = Apply(root, p); e == nil || !strings.Contains(e.Error(), "stale") {
		t.Fatalf("expected stale plan, got %v", e)
	}
	if get(t, root, "settings.json") != `{"user":"edited"}` {
		t.Fatal("lost edit")
	}
}

func TestArrayObjectIdentitySurvivesReceiptFormatting(t *testing.T) {
	root := t.TempDir()
	c := Contribution{PackageID: "plugin", Values: []Value{{Path: "opencode.jsonc", Pointer: "/plugins", Array: true, Value: map[string]any{"package": "./plugin", "options": map[string]any{"proof": "loaded"}}}}}
	apply(t, root, c)
	cs, e := Contributions(root)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Plan(root, cs)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 0 {
		t.Fatalf("array object install is not idempotent: %#v", p.Files)
	}
	apply(t, root)
	missing(t, root, "opencode.jsonc")
}

func TestReceiptPreservesReleaseMetadata(t *testing.T) {
	root := t.TempDir()
	c := Contribution{PackageID: "claude:@team/skill", Metadata: map[string]string{"version": "1.0.0", "kind": "skill", "source": "github:github.example/team/catalog#@team/skill@1.0.0", "sha256": strings.Repeat("a", 64), "harness": "claude"}, Files: []File{{Path: "skill", Data: []byte("instructions")}}}
	apply(t, root, c)
	cs, e := Contributions(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(cs) != 1 || len(cs[0].Metadata) != 5 || cs[0].Metadata["source"] != c.Metadata["source"] || cs[0].Metadata["sha256"] != c.Metadata["sha256"] {
		t.Fatalf("release provenance lost: %#v", cs)
	}
	c.Metadata["sha256"] = strings.Repeat("b", 64)
	p, e := Plan(root, []Contribution{c})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 1 || p.Files[0].Path != receiptPath {
		t.Fatalf("metadata-only change should affect receipt only: %#v", p.Files)
	}
}
