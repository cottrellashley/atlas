package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const yamlSettingsPath = ".omp/config.yml"

func yamlOwnedValue(t *testing.T, root, pointer string) json.RawMessage {
	t.Helper()
	d, err := parseConfiguration(yamlSettingsPath, []byte(get(t, root, yamlSettingsPath)))
	if err != nil {
		t.Fatal(err)
	}
	value, exists, err := d.get(pointer)
	if err != nil || !exists {
		t.Fatalf("YAML %s exists=%v error=%v", pointer, exists, err)
	}
	return value
}

func TestYAMLOwnershipPreservesNativeConfiguration(t *testing.T) {
	root := t.TempDir()
	original := "# User preferences\nmodel: 'company/opaque:model' # Keep provider selection\ntheme: \"dark\"\ncustom: |\n  Preserve this text.\n  And this line.\nskills:\n  paths:\n    - 'user/skill' # User skill\n"
	put(t, root, yamlSettingsPath, original, 0640)
	shared := Value{Path: yamlSettingsPath, Pointer: "/skills/paths", Value: "team/research", Array: true}
	a := Contribution{PackageID: "research", Values: []Value{shared, {Path: yamlSettingsPath, Pointer: "/mcp/docs/command", Value: []string{"docs-server", "--read-only"}}}}
	b := Contribution{PackageID: "audit", Values: []Value{shared}}
	apply(t, root, a, b)
	installed := get(t, root, yamlSettingsPath)
	for _, text := range []string{"# User preferences", "'company/opaque:model'", "# Keep provider selection", `theme: "dark"`, "custom: |", "Preserve this text.", "# User skill", "'user/skill'"} {
		if !strings.Contains(installed, text) {
			t.Fatalf("lost native YAML formatting/comment %q:\n%s", text, installed)
		}
	}
	info, err := os.Stat(filepath.Join(root, yamlSettingsPath))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("YAML permissions changed: %v %v", info, err)
	}
	if !jsonEqual(yamlOwnedValue(t, root, "/skills/paths"), []byte(`["user/skill","team/research"]`)) {
		t.Fatal("native sequence was not extended")
	}
	if p := apply(t, root, a, b); len(p.Files) != 0 || get(t, root, yamlSettingsPath) != installed {
		t.Fatalf("reinstall changed no-op YAML bytes: %#v", p.Files)
	}
	// Native user configuration can change independently while ownership stays
	// attached only to the installed fields and exact array entries.
	edited := strings.Replace(installed, `theme: "dark"`, `theme: "light" # changed by user`, 1)
	put(t, root, yamlSettingsPath, edited, 0640)
	if drift, err := Status(root); err != nil || len(drift) != 0 {
		t.Fatalf("unrelated YAML edit reported as drift: %#v %v", drift, err)
	}
	a.Values[1].Value = []string{"docs-server", "--v2"}
	apply(t, root, a, b)
	if !jsonEqual(yamlOwnedValue(t, root, "/mcp/docs/command"), []byte(`["docs-server","--v2"]`)) {
		t.Fatal("managed YAML update missing")
	}
	apply(t, root, b)
	if !jsonEqual(yamlOwnedValue(t, root, "/skills/paths"), []byte(`["user/skill","team/research"]`)) {
		t.Fatal("shared YAML resource removed while still owned")
	}
	apply(t, root)
	final := get(t, root, yamlSettingsPath)
	if !strings.Contains(final, `theme: "light" # changed by user`) || !strings.Contains(final, "# User skill") || !strings.Contains(final, "custom: |") {
		t.Fatalf("removal discarded unrelated YAML edits or comments:\n%s", final)
	}
	if !jsonEqual(yamlOwnedValue(t, root, "/skills/paths"), []byte(`["user/skill"]`)) {
		t.Fatal("preexisting native YAML array value removed")
	}
	d, err := parseConfiguration(yamlSettingsPath, []byte(final))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := d.get("/mcp"); err != nil || exists {
		t.Fatalf("created YAML parent not cleaned up: %v %v", exists, err)
	}
}

func TestYAMLPreexistingValueAndNoOpAreByteIdentical(t *testing.T) {
	root := t.TempDir()
	original := "# Existing native config\npermissions: { mode: 'ask' } # retain flow style\npaths: [\"same\", 'user']\n"
	put(t, root, yamlSettingsPath, original, 0600)
	c := Contribution{PackageID: "same", Values: []Value{
		{Path: yamlSettingsPath, Pointer: "/permissions/mode", Value: "ask"},
		{Path: yamlSettingsPath, Pointer: "/paths", Value: "same", Array: true},
	}}
	apply(t, root, c)
	if got := get(t, root, yamlSettingsPath); got != original {
		t.Fatalf("claiming identical values reformatted YAML:\n%s", got)
	}
	apply(t, root)
	if got := get(t, root, yamlSettingsPath); got != original {
		t.Fatalf("removing claim to preexisting values changed YAML:\n%s", got)
	}
}

func TestYAMLNewDocumentCleanupAndUserEdits(t *testing.T) {
	for _, edit := range []string{"", "field", "comment"} {
		t.Run(edit, func(t *testing.T) {
			root := t.TempDir()
			c := Contribution{PackageID: "one", Values: []Value{{Path: yamlSettingsPath, Pointer: "/tools/names", Value: "research", Array: true}}}
			apply(t, root, c)
			if edit == "field" {
				d, err := parseConfiguration(yamlSettingsPath, []byte(get(t, root, yamlSettingsPath)))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = d.set("/tools/user", []byte("true")); err != nil {
					t.Fatal(err)
				}
				data, err := d.bytes()
				if err != nil {
					t.Fatal(err)
				}
				put(t, root, yamlSettingsPath, string(data), 0600)
			} else if edit == "comment" {
				put(t, root, yamlSettingsPath, "# User note\n"+get(t, root, yamlSettingsPath), 0600)
			}
			apply(t, root)
			switch edit {
			case "":
				missing(t, root, yamlSettingsPath)
			case "field":
				if !jsonEqual(yamlOwnedValue(t, root, "/tools/user"), []byte("true")) {
					t.Fatal("user field lost during parent cleanup")
				}
			case "comment":
				if !strings.Contains(get(t, root, yamlSettingsPath), "# User note") {
					t.Fatal("user comment lost during parent cleanup")
				}
			}
		})
	}
}

func TestYAMLRejectsUnsupportedInputWithoutWriting(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":          "a: 1\na: 2\n",
		"nested duplicate":   "a:\n  b: 1\n  b: 2\n",
		"anchor":             "a: &keep {x: 1}\n",
		"alias":              "a: &keep {x: 1}\nb: *keep\n",
		"merge":              "a: {<<: {x: 1}}\n",
		"custom scalar tag":  "a: !secret value\n",
		"custom mapping tag": "a: !configuration {x: 1}\n",
		"timestamp":          "date: 2026-10-22\n",
		"binary":             "a: !!binary SGVsbG8=\n",
		"nonstring key":      "true: yes\n",
		"collection key":     "? [one, two]\n: value\n",
		"multidoc":           "a: 1\n---\nb: 2\n",
		"empty second doc":   "a: 1\n---\n",
		"sequence root":      "- one\n",
		"null root":          "null\n",
		"nonfinite":          "a: .inf\n",
		"nan":                "a: .nan\n",
		"directive":          "%YAML 1.1\n---\na: yes\n",
		"deep":               "a: " + strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, yamlSettingsPath, input, 0640)
			_, err := Plan(root, []Contribution{{PackageID: "one", Values: []Value{{Path: yamlSettingsPath, Pointer: "/owned", Value: true}}}})
			if err == nil {
				t.Fatalf("unsupported YAML accepted:\n%s", input)
			}
			if got := get(t, root, yamlSettingsPath); got != input {
				t.Fatal("rejected YAML was modified")
			}
			missing(t, root, receiptPath)
		})
	}
}

func TestYAMLDriftBlocksRemovalAndUpdate(t *testing.T) {
	root := t.TempDir()
	c := Contribution{PackageID: "one", Values: []Value{{Path: yamlSettingsPath, Pointer: "/owned", Value: "original"}}}
	apply(t, root, c)
	put(t, root, yamlSettingsPath, "owned: edited\nother: keep\n", 0600)
	if drift, err := Status(root); err != nil || len(drift) != 1 || drift[0].Reason != "configuration value differs" {
		t.Fatalf("YAML ownership drift: %#v %v", drift, err)
	}
	for _, contributions := range [][]Contribution{nil, {c}} {
		if _, err := Plan(root, contributions); err == nil {
			t.Fatal("edited YAML ownership was overwritten")
		}
	}
	if get(t, root, yamlSettingsPath) != "owned: edited\nother: keep\n" {
		t.Fatal("drift failure changed YAML")
	}
}

func TestYAMLScalarTypesPointersAndCommentOnlyFiles(t *testing.T) {
	d, err := parseYAMLDocument([]byte("# Start here\n"))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := d.bytes(); err != nil || string(data) != "# Start here\n" {
		t.Fatalf("comment-only no-op: %q %v", data, err)
	}
	values := map[string]json.RawMessage{
		"/path~1with~0escape": []byte(`"false"`),
		"/number":             []byte(`1e2`),
		"/large":              []byte(`18446744073709551615`),
		"/enabled":            []byte(`true`),
		"/nothing":            []byte(`null`),
		"/value":              []byte(`{"nested":[1,"2026-10-22",false]}`),
	}
	for pointer, value := range values {
		if _, err := d.set(pointer, value); err != nil {
			t.Fatalf("set %s: %v", pointer, err)
		}
	}
	data, err := d.bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# Start here") {
		t.Fatal("initial YAML comment was lost")
	}
	reparsed, err := parseYAMLDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	for pointer, expected := range values {
		actual, exists, err := reparsed.get(pointer)
		if err != nil || !exists || !jsonEqual(actual, expected) {
			t.Fatalf("scalar type/precision changed at %s: got %s want %s error=%v", pointer, actual, expected, err)
		}
	}
}
